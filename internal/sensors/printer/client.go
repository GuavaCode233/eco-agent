// Package printer 實作路徑 B：印表機用紙量感測（CLAUDE.md Step 3）。
//
// 歸戶前提（v15）：本階段只做「個人專屬印表機 SNMP 輪詢歸戶」一軌——印表機與員工一對一，
// 故 page counter 讀數可直接歸給本機綁定的 ID Token。共用機的 Print Server Log / Pull
// Printing API 屬「未來實作」不在範圍；手動上傳用紙量屬 App 端、非 Agent 路徑。
//
// SNMP 查詢（v0.23 [D15] 修訂後）：
//   - page counter：SNMP v2c（UDP 161）查 OID 1.3.6.1.2.1.43.10.2.1.4（prtMarkerLifeCount，
//     壽命累計值），**原樣上送、不在本機相減**——差分與 counter 重置防呆皆移至後端
//     （見 sensor.go；baseline 若只活在 Agent 本機，重裝／換機／佇列毀損即遺失）；
//   - printer_serial：供 [D14] 缺口二的 per-printer 歸鍵用，依序試三個候選 OID：
//     prtGeneralSerialNumber（Printer-MIB，首選）→ entPhysicalSerialNum（ENTITY-MIB，次選）
//     → sysName（末選，管理員可改、不保證唯一）；可用 ECO_AGENT_PRINTER_SERIAL_OID 指定
//     單一 OID 跳過候選鏈；
//   - 目標位址／community 由環境變數提供，未設定時回 ErrNotConfigured 供上層優雅降級。
//
// 感測模式、送出與 BYOD 摩擦點見 sensor.go（3.2–3.4）。
package printer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// OIDPageCounterColumn 為 Printer MIB 的 prtMarkerLifeCount 欄（RFC 1759 / RFC 3805）：
// 印表機自出廠以來的累計輸出頁數，只增不減、無推播，故只能輪詢後相減取增量。
//
// 這是「欄」而非單一 instance；實際值掛在 <欄>.<hrDeviceIndex>.<prtMarkerIndex>，
// 個人印表機幾乎恆為 .1.1（見 DefaultPageCounterOID）。
const OIDPageCounterColumn = "1.3.6.1.2.1.43.10.2.1.4"

// DefaultPageCounterOID 為絕大多數單機型號的 page counter instance。
// 查不到時退回巡走整個欄（見 PageCounter），以相容 index 不為 1.1 的機種。
const DefaultPageCounterOID = OIDPageCounterColumn + ".1.1"

// 印表機序號的三個候選 OID（依序試，[D14] 缺口二：路徑 B 須以 printer_serial 而非
// device_id 歸鍵，否則桌機＋筆電同指一台專屬印表機時頁數會重複計算）。
const (
	// OIDSerialPrtGeneralColumn 為 Printer-MIB 的 prtGeneralSerialNumber 欄（首選）。
	OIDSerialPrtGeneralColumn = "1.3.6.1.2.1.43.5.1.1.17"
	// DefaultSerialPrtGeneralOID 為絕大多數單機型號的序號 instance。
	DefaultSerialPrtGeneralOID = OIDSerialPrtGeneralColumn + ".1"
	// OIDSerialEntPhysicalColumn 為 ENTITY-MIB 的 entPhysicalSerialNum 欄（次選）。
	OIDSerialEntPhysicalColumn = "1.3.6.1.2.1.47.1.1.1.1.11"
	// DefaultSerialEntPhysicalOID 為第一個實體（多為印表機本體）的序號 instance。
	DefaultSerialEntPhysicalOID = OIDSerialEntPhysicalColumn + ".1"
	// OIDSysName 為 sysName（末選；純量本身即 instance，管理員可改、不保證唯一，
	// 亦不套用巡走 fallback——巡走可能撈到系統群組內其他不相關欄位）。
	OIDSysName = "1.3.6.1.2.1.1.5.0"
)

// SNMP 連線預設值。
const (
	// DefaultCommunity 為 SNMP v2c 唯讀 community 的業界慣例預設值。
	DefaultCommunity = "public"
	// DefaultPort 為 SNMP agent 的標準 UDP 埠。
	DefaultPort uint16 = 161
	// DefaultTimeout 為單次查詢逾時；印表機多在同網段，逾時短一點以免拖住巡檢。
	DefaultTimeout = 3 * time.Second
	// DefaultRetries 為逾時後的重送次數（UDP 無連線保證，補一次即可）。
	DefaultRetries = 1
)

// 環境變數名稱（目標印表機由部署者提供；BYOD 情境下每台機器不同）。
//
// TODO(backend): 目標印表機位址日後可能改由 5.2 集中配置服務隨 sensor_config 下發
// （或由 App 端綁定流程寫入），屆時本環境變數退居為本機覆寫。
const (
	// EnvHost：印表機 IP 或主機名。未設定即視為「本機無個人專屬印表機」，路徑 B 跳過。
	EnvHost = "ECO_AGENT_PRINTER_HOST"
	// EnvPort：SNMP 埠（可選，預設 161）。
	EnvPort = "ECO_AGENT_PRINTER_PORT"
	// EnvCommunity：SNMP v2c 唯讀 community（可選，預設 public）。
	EnvCommunity = "ECO_AGENT_PRINTER_COMMUNITY"
	// EnvOID：page counter 的 instance OID（可選，預設 DefaultPageCounterOID）。
	// 供 index 非 1.1、或改用廠商私有 counter 的機種覆寫。
	EnvOID = "ECO_AGENT_PRINTER_OID"
	// EnvSerialOID：印表機序號 OID（可選）。設定時只查此單一 OID（略過三候選 fallback
	// 鏈），供 [D14] 歸鍵用；未設則依序試 prtGeneralSerialNumber → entPhysicalSerialNum
	// → sysName。
	EnvSerialOID = "ECO_AGENT_PRINTER_SERIAL_OID"
)

var (
	// ErrNotConfigured 表示未設定 ECO_AGENT_PRINTER_HOST——本機沒有個人專屬印表機。
	// 路徑 B 應優雅降級（記 log、跳過），不使整個 Agent 崩潰（§2 Step 3.4）。
	ErrNotConfigured = errors.New("printer: target not configured (set ECO_AGENT_PRINTER_HOST)")
	// ErrNoPageCounter 表示裝置有回應，但取不到可用的 page counter
	// （OID 不存在且巡走 prtMarkerLifeCount 欄亦無結果）——多半不是印表機或未開啟 SNMP。
	ErrNoPageCounter = errors.New("printer: no usable page counter (prtMarkerLifeCount) on target")
	// ErrNoSerialNumber 表示三個候選 OID（或明確指定的單一 OID）皆查無序號。
	// 呼叫端（sensor.go）應降級：省略 payload 的 printer_serial 欄位，由後端以
	// device_id 回退歸鍵並標記「印表機身份不明」（[D14] 缺口二）。
	ErrNoSerialNumber = errors.New("printer: no usable serial number (prtGeneralSerialNumber/entPhysicalSerialNum/sysName) on target")
)

// PageCounterSampler 抽象「取印表機累計頁數與序號」，供 3.2 感測器以介面注入，便於測試
// （fake 或 MockAgent 滿足）並維持感測器對 SNMP 細節的最小依賴面。真實實作為 *SNMPClient。
//
// SerialNumber 供 [D14] 缺口二的 per-printer 歸鍵用；查無序號時回 ErrNoSerialNumber，
// 由呼叫端決定降級方式（省略 payload 的 printer_serial 欄位）。
type PageCounterSampler interface {
	PageCounter(ctx context.Context) (int64, error)
	SerialNumber(ctx context.Context) (string, error)
}

// SNMPClient 以 SNMP v2c 向個人專屬印表機查 page counter 累計值與序號。
//
// 無狀態、每次查詢自建連線：印表機輪詢區間為分鐘級（printerPollInterval），
// 常駐 UDP socket 無益處，反而在網路切換／印表機重啟後容易殘留失效狀態。
type SNMPClient struct {
	host      string
	port      uint16
	community string
	oid       string
	serialOID string // 空字串＝走三候選 fallback 鏈（見 SerialNumber）；否則只查此 OID
	timeout   time.Duration
	retries   int
}

// Option 以函式選項調整 SNMPClient（對齊 sensors/computer、sensors/drive 的 WithX 慣例）。
type Option func(*SNMPClient)

// WithPort 覆寫 SNMP 埠（預設 161）。
func WithPort(p uint16) Option {
	return func(c *SNMPClient) {
		if p > 0 {
			c.port = p
		}
	}
}

// WithCommunity 覆寫 v2c community（預設 public）。
func WithCommunity(s string) Option {
	return func(c *SNMPClient) {
		if s != "" {
			c.community = s
		}
	}
}

// WithOID 覆寫 page counter 的 instance OID（預設 DefaultPageCounterOID）。
func WithOID(oid string) Option {
	return func(c *SNMPClient) {
		if oid != "" {
			c.oid = oid
		}
	}
}

// WithSerialOID 指定序號查詢改用單一 OID（略過 SerialNumber 預設的三候選 fallback 鏈）。
func WithSerialOID(oid string) Option {
	return func(c *SNMPClient) {
		if oid != "" {
			c.serialOID = oid
		}
	}
}

// WithTimeout 覆寫單次查詢逾時。
func WithTimeout(d time.Duration) Option {
	return func(c *SNMPClient) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithRetries 覆寫逾時重送次數。
func WithRetries(n int) Option {
	return func(c *SNMPClient) {
		if n >= 0 {
			c.retries = n
		}
	}
}

// NewSNMPClient 以印表機主機位址建立客戶端。host 為空回 ErrNotConfigured。
func NewSNMPClient(host string, opts ...Option) (*SNMPClient, error) {
	if host == "" {
		return nil, ErrNotConfigured
	}
	c := &SNMPClient{
		host:      host,
		port:      DefaultPort,
		community: DefaultCommunity,
		oid:       DefaultPageCounterOID,
		timeout:   DefaultTimeout,
		retries:   DefaultRetries,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// NewSNMPClientFromEnv 由環境變數建立客戶端；未設 EnvHost 回 ErrNotConfigured，
// 供上層（wiring）判斷「本機無個人專屬印表機」而跳過路徑 B。
// opts 於環境變數之後套用，供呼叫端強制覆寫。
func NewSNMPClientFromEnv(opts ...Option) (*SNMPClient, error) {
	host := os.Getenv(EnvHost)
	if host == "" {
		return nil, ErrNotConfigured
	}
	envOpts := []Option{
		WithCommunity(os.Getenv(EnvCommunity)),
		WithOID(os.Getenv(EnvOID)),
		WithSerialOID(os.Getenv(EnvSerialOID)),
	}
	if p := os.Getenv(EnvPort); p != "" {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("printer: invalid %s=%q: %w", EnvPort, p, err)
		}
		envOpts = append(envOpts, WithPort(uint16(n)))
	}
	return NewSNMPClient(host, append(envOpts, opts...)...)
}

// Target 回傳目前查詢目標（host:port 與 OID），供 log／demo 顯示。
func (c *SNMPClient) Target() (addr, oid string) {
	return fmt.Sprintf("%s:%d", c.host, c.port), c.oid
}

// connect 建立一次性 SNMP 連線。無狀態、每次查詢自建：印表機輪詢區間為分鐘級
// （printerPollInterval），常駐 UDP socket 無益處，反而在網路切換／印表機重啟後
// 容易殘留失效狀態。呼叫端負責 defer g.Conn.Close()。
func (c *SNMPClient) connect(ctx context.Context) (*gosnmp.GoSNMP, error) {
	g := &gosnmp.GoSNMP{
		Context:   ctx,
		Target:    c.host,
		Port:      c.port,
		Community: c.community,
		Version:   gosnmp.Version2c,
		Timeout:   c.timeout,
		Retries:   c.retries,
		MaxOids:   gosnmp.MaxOids,
	}
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("printer: connect %s:%d: %w", c.host, c.port, err)
	}
	return g, nil
}

// PageCounter 取得印表機目前的累計輸出頁數（prtMarkerLifeCount）。
//
// 原樣回傳壽命累計絕對值，**不在本機相減**——區間差分與 counter 重置防呆移至後端
// （v0.23 [D15]，見 sensor.go）。先直接 GET 設定的 instance OID；若該 instance 不存在
// （noSuchInstance／noSuchObject，常見於 index 非 1.1 的機種），退回巡走 prtMarkerLifeCount
// 欄取第一筆可用值。兩者皆無回 ErrNoPageCounter；連線／逾時錯誤原樣包裝回傳，由呼叫端
// 記 log 並下次輪詢重試。
func (c *SNMPClient) PageCounter(ctx context.Context) (int64, error) {
	g, err := c.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer g.Conn.Close()

	// 1) 直接查設定的 instance。
	res, err := g.Get([]string{c.oid})
	if err != nil {
		return 0, fmt.Errorf("printer: snmp get %s: %w", c.oid, err)
	}
	for _, pdu := range res.Variables {
		if v, ok := counterValue(pdu); ok {
			return v, nil
		}
	}

	// 2) instance 不存在：巡走整個 prtMarkerLifeCount 欄，取第一筆可用值。
	//    個人專屬機為一對一歸戶，多 marker 機種取第一組即代表其輸出量。
	pdus, err := g.WalkAll(OIDPageCounterColumn)
	if err != nil {
		return 0, fmt.Errorf("printer: snmp walk %s: %w", OIDPageCounterColumn, err)
	}
	for _, pdu := range pdus {
		if v, ok := counterValue(pdu); ok {
			return v, nil
		}
	}
	return 0, ErrNoPageCounter
}

// counterValue 由 SNMP varbind 取出數值。noSuchObject／noSuchInstance／endOfMibView／null
// 等「無值」型別回 ok=false；負值（廠商回 Integer 且異常）亦視為不可用。
func counterValue(pdu gosnmp.SnmpPDU) (int64, bool) {
	switch pdu.Type {
	case gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32, gosnmp.Integer, gosnmp.Uinteger32:
		n := gosnmp.ToBigInt(pdu.Value)
		if n == nil || !n.IsInt64() {
			return 0, false
		}
		v := n.Int64()
		if v < 0 {
			return 0, false
		}
		return v, true
	default:
		return 0, false
	}
}

// SerialNumber 取得印表機序號，供 [D14] 缺口二的 per-printer 歸鍵用。
//
// 明確設定 serialOID（WithSerialOID／EnvSerialOID）時只查該單一 OID，不做候選鏈。
// 否則依序試三個候選：prtGeneralSerialNumber（Printer-MIB，首選，GET 落空時巡走整欄，
// 相容 index 非 1 的機種）→ entPhysicalSerialNum（ENTITY-MIB，次選，同樣巡走）→
// sysName（末選，純量本身即 instance，管理員可改、不保證唯一，不套巡走——巡走可能撈到
// 系統群組內其他不相關欄位）。三者皆查無值時回 ErrNoSerialNumber，由呼叫端決定降級方式。
func (c *SNMPClient) SerialNumber(ctx context.Context) (string, error) {
	if c.serialOID != "" {
		v, err := c.getStringInstance(ctx, c.serialOID)
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", ErrNoSerialNumber
		}
		return v, nil
	}

	if v, err := c.getStringWithWalkFallback(ctx, DefaultSerialPrtGeneralOID, OIDSerialPrtGeneralColumn); err == nil && v != "" {
		return v, nil
	}
	if v, err := c.getStringWithWalkFallback(ctx, DefaultSerialEntPhysicalOID, OIDSerialEntPhysicalColumn); err == nil && v != "" {
		return v, nil
	}
	if v, err := c.getStringInstance(ctx, OIDSysName); err == nil && v != "" {
		return v, nil
	}
	return "", ErrNoSerialNumber
}

// getStringInstance 直接 GET 單一 OID 取字串值；查無值（而非連線失敗）回 ("", nil)，
// 供候選鏈呼叫端判斷是否嘗試下一候選。
func (c *SNMPClient) getStringInstance(ctx context.Context, oid string) (string, error) {
	g, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer g.Conn.Close()
	res, err := g.Get([]string{oid})
	if err != nil {
		return "", fmt.Errorf("printer: snmp get %s: %w", oid, err)
	}
	for _, pdu := range res.Variables {
		if v, ok := stringValue(pdu); ok {
			return v, nil
		}
	}
	return "", nil
}

// getStringWithWalkFallback 先 GET instanceOID；不存在則巡走 column 欄取第一筆可用值
// （比照 PageCounter 對 index 非 1 機種的相容做法）。查無值（而非連線失敗）回 ("", nil)。
func (c *SNMPClient) getStringWithWalkFallback(ctx context.Context, instanceOID, column string) (string, error) {
	g, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer g.Conn.Close()

	if res, gerr := g.Get([]string{instanceOID}); gerr == nil {
		for _, pdu := range res.Variables {
			if v, ok := stringValue(pdu); ok {
				return v, nil
			}
		}
	}

	pdus, werr := g.WalkAll(column)
	if werr != nil {
		return "", fmt.Errorf("printer: snmp walk %s: %w", column, werr)
	}
	for _, pdu := range pdus {
		if v, ok := stringValue(pdu); ok {
			return v, nil
		}
	}
	return "", nil
}

// stringValue 由 SNMP varbind 取出可用的字串值（序號多為 OCTET STRING）。
// noSuchObject／noSuchInstance／endOfMibView／null 等「無值」型別回 ok=false；
// 空白字串（裝置回應存在但值為空）視為「無此值」。
func stringValue(pdu gosnmp.SnmpPDU) (string, bool) {
	if pdu.Type != gosnmp.OctetString {
		return "", false
	}
	b, ok := pdu.Value.([]byte)
	if !ok {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	return s, s != ""
}

// 確保 SNMPClient 滿足 PageCounterSampler 介面。
var _ PageCounterSampler = (*SNMPClient)(nil)
