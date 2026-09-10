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

// 連續兩次輪詢：兩次都應原樣回傳當下的絕對讀數（差分移至後端，[D15]）。
func TestPageCounterReadsAbsoluteValueAcrossPolls(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1000})
	ctx := context.Background()

	first, err := c.PageCounter(ctx)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if first != 1000 {
		t.Errorf("first poll = %d, want 1000", first)
	}
	agent.SetValue(DefaultPageCounterOID, 1007) // 期間列印 7 頁
	second, err := c.PageCounter(ctx)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if second != 1007 {
		t.Errorf("second poll = %d, want 1007（原樣讀出絕對值，不在本機相減）", second)
	}
}

// ── 序號（[D14] 缺口二）──

// 首選 OID（prtGeneralSerialNumber）直接 GET 命中。
func TestSerialNumberPrtGeneral(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1})
	agent.SetString(DefaultSerialPrtGeneralOID, "SN-PRTGEN-001")

	got, err := c.SerialNumber(context.Background())
	if err != nil {
		t.Fatalf("SerialNumber: %v", err)
	}
	if got != "SN-PRTGEN-001" {
		t.Errorf("SerialNumber = %q, want SN-PRTGEN-001", got)
	}
}

// 首選 instance 不存在但整欄有值（index 非 1 的機種）：巡走取得。
func TestSerialNumberPrtGeneralWalkFallback(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1})
	agent.SetString(OIDSerialPrtGeneralColumn+".2", "SN-WALK-002")

	got, err := c.SerialNumber(context.Background())
	if err != nil {
		t.Fatalf("SerialNumber: %v", err)
	}
	if got != "SN-WALK-002" {
		t.Errorf("SerialNumber = %q, want SN-WALK-002", got)
	}
}

// 首選整欄皆空：退回次選 entPhysicalSerialNum。
func TestSerialNumberFallbackToEntPhysical(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1})
	agent.SetString(DefaultSerialEntPhysicalOID, "SN-ENTPHYS-003")

	got, err := c.SerialNumber(context.Background())
	if err != nil {
		t.Fatalf("SerialNumber: %v", err)
	}
	if got != "SN-ENTPHYS-003" {
		t.Errorf("SerialNumber = %q, want SN-ENTPHYS-003", got)
	}
}

// 前兩者皆空：末選 sysName（純量，不套巡走）。
func TestSerialNumberFallbackToSysName(t *testing.T) {
	c, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1})
	agent.SetString(OIDSysName, "printer-hostname")

	got, err := c.SerialNumber(context.Background())
	if err != nil {
		t.Fatalf("SerialNumber: %v", err)
	}
	if got != "printer-hostname" {
		t.Errorf("SerialNumber = %q, want printer-hostname", got)
	}
}

// 三個候選皆空：回 ErrNoSerialNumber，供呼叫端降級（省略 printer_serial，[D14]）。
func TestSerialNumberAllEmpty(t *testing.T) {
	c, _ := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 1})

	if _, err := c.SerialNumber(context.Background()); !errors.Is(err, ErrNoSerialNumber) {
		t.Fatalf("err = %v, want ErrNoSerialNumber", err)
	}
}

// 明確指定 WithSerialOID 時只查該單一 OID，略過三候選 fallback 鏈。
func TestSerialNumberExplicitOIDSkipsFallbackChain(t *testing.T) {
	agent, err := StartMockAgent("127.0.0.1:0", DefaultCommunity, map[string]uint64{DefaultPageCounterOID: 1})
	if err != nil {
		t.Fatalf("start mock agent: %v", err)
	}
	t.Cleanup(func() { agent.Close() })
	// 候選鏈的首選有值，但明確指定的 OID 應該才是實際被查的那個。
	agent.SetString(DefaultSerialPrtGeneralOID, "SN-SHOULD-NOT-BE-USED")
	agent.SetString("1.3.6.1.4.1.99999.1", "SN-CUSTOM-VENDOR")

	host, port := agent.Addr()
	c, err := NewSNMPClient(host, WithPort(port), WithTimeout(2*time.Second), WithRetries(1),
		WithSerialOID("1.3.6.1.4.1.99999.1"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	got, err := c.SerialNumber(context.Background())
	if err != nil {
		t.Fatalf("SerialNumber: %v", err)
	}
	if got != "SN-CUSTOM-VENDOR" {
		t.Errorf("SerialNumber = %q, want SN-CUSTOM-VENDOR（應只查明確指定的 OID）", got)
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
	t.Setenv(EnvSerialOID, "1.3.6.1.4.1.99999.1")

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
	if c.serialOID != "1.3.6.1.4.1.99999.1" {
		t.Errorf("serialOID = %q, want 1.3.6.1.4.1.99999.1", c.serialOID)
	}
}

// 可選環境變數未設時沿用預設值（serialOID 留空 = 走三候選 fallback 鏈）。
func TestNewSNMPClientFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvHost, "printer.local")
	t.Setenv(EnvPort, "")
	t.Setenv(EnvCommunity, "")
	t.Setenv(EnvOID, "")
	t.Setenv(EnvSerialOID, "")

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
	if c.serialOID != "" {
		t.Errorf("serialOID = %q, want empty（走候選鏈）", c.serialOID)
	}
}

func TestNewSNMPClientFromEnvInvalidPort(t *testing.T) {
	t.Setenv(EnvHost, "printer.local")
	t.Setenv(EnvPort, "not-a-port")

	if _, err := NewSNMPClientFromEnv(); err == nil {
		t.Fatal("無效的埠應回錯誤，實際回 nil")
	}
}
