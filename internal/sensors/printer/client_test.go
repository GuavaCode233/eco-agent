package printer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newTestClient 啟一個本機 mock SNMP 代理並回傳指向它的客戶端。
func newTestClient(t *testing.T, values map[string]uint64, opts ...Option) (*SNMPClient, *MockAgent) {
	t.Helper()
	agent, err := StartMockAgent("127.0.0.1:0", DefaultCommunity, values)
	if err != nil {
		t.Fatalf("start mock agent: %v", err)
	}
	t.Cleanup(func() { agent.Close() })

	host, port := agent.Addr()
	base := []Option{WithPort(port), WithTimeout(2 * time.Second), WithRetries(1)}
	c, err := NewSNMPClient(host, append(base, opts...)...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c, agent
}

// 直接 GET 設定的 instance OID 取得累計頁數。
func TestPageCounterGet(t *testing.T) {
	c, _ := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 12345})

	got, err := c.PageCounter(context.Background())
	if err != nil {
		t.Fatalf("PageCounter: %v", err)
	}
	if got != 12345 {
		t.Errorf("PageCounter = %d, want 12345", got)
	}
}

// instance 不為 .1.1 的機種：GET 落空後應退回巡走 prtMarkerLifeCount 欄取到值。
func TestPageCounterWalkFallback(t *testing.T) {
	c, _ := newTestClient(t, map[string]uint64{
		OIDPageCounterColumn + ".1.2": 777,
	})

	got, err := c.PageCounter(context.Background())
	if err != nil {
		t.Fatalf("PageCounter: %v", err)
	}
	if got != 777 {
		t.Errorf("PageCounter = %d, want 777（應由 walk fallback 取得）", got)
	}
}

// 多 marker 機種：巡走時取排序最前的一筆（.1.2 應排在 .1.10 之前，非字串字典序）。
func TestPageCounterWalkPicksFirstByNumericOrder(t *testing.T) {
	c, _ := newTestClient(t, map[string]uint64{
		OIDPageCounterColumn + ".1.10": 999,
		OIDPageCounterColumn + ".1.2":  111,
	})

	got, err := c.PageCounter(context.Background())
	if err != nil {
		t.Fatalf("PageCounter: %v", err)
	}
	if got != 111 {
		t.Errorf("PageCounter = %d, want 111（.1.2 應排在 .1.10 之前）", got)
	}
}

// 有回應但沒有任何可用 page counter（非印表機／未開 SNMP）→ ErrNoPageCounter。
func TestPageCounterNoUsableCounter(t *testing.T) {
	c, _ := newTestClient(t, map[string]uint64{"1.3.6.1.2.1.1.3.0": 42})

	_, err := c.PageCounter(context.Background())
	if !errors.Is(err, ErrNoPageCounter) {
		t.Fatalf("err = %v, want ErrNoPageCounter", err)
	}
}

// 印表機不在同網段／未開機：查詢逾時應回錯誤而非卡住（3.4 摩擦點的前提）。
func TestPageCounterUnreachable(t *testing.T) {
	agent, err := StartMockAgent("127.0.0.1:0", DefaultCommunity, nil)
	if err != nil {
		t.Fatalf("start mock agent: %v", err)
	}
	host, port := agent.Addr()
	agent.Close() // 立刻關閉，讓該埠無人回應

	c, err := NewSNMPClient(host, WithPort(port), WithTimeout(300*time.Millisecond), WithRetries(0))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.PageCounter(context.Background()); err == nil {
		t.Fatal("PageCounter 應回逾時錯誤，實際回 nil")
	}
}

// community 不符：mock 代理丟棄請求，客戶端逾時。
func TestPageCounterWrongCommunity(t *testing.T) {
	c, _ := newTestClient(t,
		map[string]uint64{DefaultPageCounterOID: 1},
		WithCommunity("wrong"), WithTimeout(300*time.Millisecond), WithRetries(0))

	if _, err := c.PageCounter(context.Background()); err == nil {
		t.Fatal("community 不符時應逾時失敗，實際成功")
	}
}

// 連續兩次輪詢：模擬期間列印若干頁，增量應為兩次累計值之差。
func TestPageCounterDeltaAcrossPolls(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1000})
	ctx := context.Background()

	prev, err := c.PageCounter(ctx)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	agent.SetValue(DefaultPageCounterOID, 1007) // 期間列印 7 頁
	cur, err := c.PageCounter(ctx)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if got := PageDelta(prev, cur); got != 7 {
		t.Errorf("PageDelta(%d, %d) = %d, want 7", prev, cur, got)
	}
}

func TestPageDelta(t *testing.T) {
	tests := []struct {
		name      string
		prev, cur int64
		want      int64
	}{
		{"一般增量", 1000, 1007, 7},
		{"無列印", 1000, 1000, 0},
		{"首次輪詢無基準", -1, 500, 0},
		{"counter 重置（換機/韌體重置）不回填", 5000, 12, 0},
		{"自零起算", 0, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PageDelta(tt.prev, tt.cur); got != tt.want {
				t.Errorf("PageDelta(%d, %d) = %d, want %d", tt.prev, tt.cur, got, tt.want)
			}
		})
	}
}

// 未設 ECO_AGENT_PRINTER_HOST：回 ErrNotConfigured 供上層優雅降級跳過路徑 B。
func TestNewSNMPClientFromEnvNotConfigured(t *testing.T) {
	t.Setenv(EnvHost, "")

	if _, err := NewSNMPClientFromEnv(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestNewSNMPClientFromEnv(t *testing.T) {
	t.Setenv(EnvHost, "192.0.2.10")
	t.Setenv(EnvPort, "1610")
	t.Setenv(EnvCommunity, "office")
	t.Setenv(EnvOID, OIDPageCounterColumn+".1.3")

	c, err := NewSNMPClientFromEnv()
	if err != nil {
		t.Fatalf("NewSNMPClientFromEnv: %v", err)
	}
	addr, oid := c.Target()
	if addr != "192.0.2.10:1610" {
		t.Errorf("addr = %q, want 192.0.2.10:1610", addr)
	}
	if oid != OIDPageCounterColumn+".1.3" {
		t.Errorf("oid = %q, want %s.1.3", oid, OIDPageCounterColumn)
	}
	if c.community != "office" {
		t.Errorf("community = %q, want office", c.community)
	}
}

// 可選環境變數未設時沿用預設值。
func TestNewSNMPClientFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvHost, "printer.local")
	t.Setenv(EnvPort, "")
	t.Setenv(EnvCommunity, "")
	t.Setenv(EnvOID, "")

	c, err := NewSNMPClientFromEnv()
	if err != nil {
		t.Fatalf("NewSNMPClientFromEnv: %v", err)
	}
	addr, oid := c.Target()
	if addr != "printer.local:161" {
		t.Errorf("addr = %q, want printer.local:161", addr)
	}
	if oid != DefaultPageCounterOID {
		t.Errorf("oid = %q, want %s", oid, DefaultPageCounterOID)
	}
	if c.community != DefaultCommunity {
		t.Errorf("community = %q, want %s", c.community, DefaultCommunity)
	}
}

func TestNewSNMPClientFromEnvInvalidPort(t *testing.T) {
	t.Setenv(EnvHost, "printer.local")
	t.Setenv(EnvPort, "not-a-port")

	if _, err := NewSNMPClientFromEnv(); err == nil {
		t.Fatal("無效的埠應回錯誤，實際回 nil")
	}
}
