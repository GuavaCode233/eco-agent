// MOCK: 本檔提供一個極簡的本機 SNMP v2c responder，模擬印表機回應 page counter，
// 供 Step 3.V「對本機 mock SNMP responder 輪詢，確認增量頁數正確」與單元測試使用。
//
// 定位同 internal/uploader/mockserver.go：屬開發/驗證工具，不參與正式採集路徑；
// 正式路徑一律以 gosnmp 對真實印表機查詢（見 client.go）。
//
// 支援子集：SNMP v2c 的 GetRequest / GetNextRequest / GetBulkRequest，回應 Counter32；
// 查無此 instance 回 noSuchInstance、走到表尾回 endOfMibView。不支援 v1/v3、Set、Trap。
package printer

import (
	"fmt"
	"net"
	"sort"
	"sync"
)

// MockAgent 是監聽 UDP 的假 SNMP 代理，持有一組 OID → 累計值。
type MockAgent struct {
	conn      *net.UDPConn
	community string

	mu     sync.Mutex
	values map[string]uint64

	closeOnce sync.Once
	closed    chan struct{}
}

// StartMockAgent 於 addr（如 "127.0.0.1:0" 讓 OS 配埠）啟動假 SNMP 代理。
//
// community 為預期的 v2c community（空字串時預設 DefaultCommunity）；不符的請求直接丟棄，
// 模擬真實代理的行為（請求端會逾時）。values 為初始 OID → 累計值，會被複製一份。
func StartMockAgent(addr, community string, values map[string]uint64) (*MockAgent, error) {
	if community == "" {
		community = DefaultCommunity
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("printer: mock agent resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("printer: mock agent listen %q: %w", addr, err)
	}
	a := &MockAgent{
		conn:      conn,
		community: community,
		values:    make(map[string]uint64, len(values)),
		closed:    make(chan struct{}),
	}
	for k, v := range values {
		a.values[k] = v
	}
	go a.serve()
	return a, nil
}

// Addr 回傳實際監聽的位址與埠（addr 用 :0 時取得 OS 配的埠）。
func (a *MockAgent) Addr() (host string, port uint16) {
	ua := a.conn.LocalAddr().(*net.UDPAddr)
	return ua.IP.String(), uint16(ua.Port)
}

// SetValue 設定（或新增）某 OID 的累計值，供測試模擬「列印後 page counter 增加」。
func (a *MockAgent) SetValue(oid string, v uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.values[oid] = v
}

// Close 停止代理並釋放 socket。
func (a *MockAgent) Close() error {
	var err error
	a.closeOnce.Do(func() {
		close(a.closed)
		err = a.conn.Close()
	})
	return err
}

func (a *MockAgent) serve() {
	buf := make([]byte, 4096)
	for {
		n, from, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-a.closed:
				return // 正常關閉
			default:
				continue
			}
		}
		resp, err := a.handle(buf[:n])
		if err != nil || resp == nil {
			continue // 解析失敗／community 不符：靜默丟棄（同真實代理）
		}
		_, _ = a.conn.WriteToUDP(resp, from)
	}
}

// handle 解析一個 SNMP 請求並組出回應；不該回應時回 (nil, nil)。
func (a *MockAgent) handle(req []byte) ([]byte, error) {
	tag, msg, _, err := readTLV(req)
	if err != nil || tag != tagSequence {
		return nil, errBER
	}
	// message ::= SEQUENCE { version INTEGER, community OCTET STRING, pdu }
	_, versionRaw, rest, err := readTLV(msg)
	if err != nil {
		return nil, err
	}
	version, err := decodeInt(versionRaw)
	if err != nil {
		return nil, err
	}
	if version != 1 { // 僅支援 v2c（version 欄位為 1）
		return nil, nil
	}
	ctag, communityRaw, rest, err := readTLV(rest)
	if err != nil || ctag != tagOctetString {
		return nil, errBER
	}
	if string(communityRaw) != a.community {
		return nil, nil
	}
	pduTag, pdu, _, err := readTLV(rest)
	if err != nil {
		return nil, err
	}

	// pdu ::= { request-id INTEGER, error-status/non-repeaters INTEGER,
	//           error-index/max-repetitions INTEGER, varbinds SEQUENCE }
	_, requestID, rest, err := readTLV(pdu)
	if err != nil {
		return nil, err
	}
	_, field2, rest, err := readTLV(rest)
	if err != nil {
		return nil, err
	}
	_, field3, rest, err := readTLV(rest)
	if err != nil {
		return nil, err
	}
	vbTag, varbinds, _, err := readTLV(rest)
	if err != nil || vbTag != tagSequence {
		return nil, errBER
	}
	oids, err := parseVarbindOIDs(varbinds)
	if err != nil {
		return nil, err
	}

	var body []byte
	switch pduTag {
	case tagGetRequest:
		for _, oid := range oids {
			body = append(body, a.getVarbind(oid)...)
		}
	case tagGetNext:
		for _, oid := range oids {
			body = append(body, a.nextVarbind(oid)...)
		}
	case tagGetBulk:
		nonRepeaters, _ := decodeInt(field2)
		maxRepetitions, _ := decodeInt(field3)
		body = a.bulkVarbinds(oids, int(nonRepeaters), int(maxRepetitions))
	default:
		return nil, nil // Set/Trap 等不支援，靜默丟棄
	}

	respPDU := tlv(tagGetResponse, concat(
		tlv(tagInteger, requestID), // 原樣回送 request-id
		tlv(tagInteger, []byte{0}), // error-status = noError
		tlv(tagInteger, []byte{0}), // error-index = 0
		tlv(tagSequence, body),
	))
	return tlv(tagSequence, concat(
		tlv(tagInteger, []byte{0x01}), // version = v2c
		tlv(tagOctetString, []byte(a.community)),
		respPDU,
	)), nil
}

// getVarbind 組出 GetRequest 的單筆回應 varbind：有值回 Counter32，無值回 noSuchInstance。
func (a *MockAgent) getVarbind(oid string) []byte {
	a.mu.Lock()
	v, ok := a.values[oid]
	a.mu.Unlock()
	if !ok {
		return varbind(oid, tlv(tagNoSuchInst, nil))
	}
	return varbind(oid, tlv(tagCounter32, encodeUint(v)))
}

// nextVarbind 組出 GetNextRequest 的單筆回應 varbind：取字典序（逐節數值）之後的第一筆；
// 已無下一筆則回 endOfMibView。
func (a *MockAgent) nextVarbind(oid string) []byte {
	next, v, ok := a.next(oid)
	if !ok {
		return varbind(oid, tlv(tagEndOfMibView, nil))
	}
	return varbind(next, tlv(tagCounter32, encodeUint(v)))
}

// bulkVarbinds 以「非重複項各取一次 next、其餘項連續取 maxRepetitions 次」組出 GetBulk 回應。
func (a *MockAgent) bulkVarbinds(oids []string, nonRepeaters, maxRepetitions int) []byte {
	if nonRepeaters < 0 {
		nonRepeaters = 0
	}
	if maxRepetitions < 1 {
		maxRepetitions = 1
	}
	var body []byte
	for i, oid := range oids {
		if i < nonRepeaters {
			body = append(body, a.nextVarbind(oid)...)
			continue
		}
		cur := oid
		for r := 0; r < maxRepetitions; r++ {
			next, v, ok := a.next(cur)
			if !ok {
				body = append(body, varbind(cur, tlv(tagEndOfMibView, nil))...)
				break
			}
			body = append(body, varbind(next, tlv(tagCounter32, encodeUint(v)))...)
			cur = next
		}
	}
	return body
}

// next 回傳排序後嚴格大於 oid 的第一筆。
func (a *MockAgent) next(oid string) (string, uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]string, 0, len(a.values))
	for k := range a.values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return compareOID(keys[i], keys[j]) < 0 })
	for _, k := range keys {
		if compareOID(k, oid) > 0 {
			return k, a.values[k], true
		}
	}
	return "", 0, false
}

// varbind 組出 VarBind ::= SEQUENCE { name OID, value }。OID 無法編碼時回空（該筆略過）。
func varbind(oid string, value []byte) []byte {
	name, err := encodeOID(oid)
	if err != nil {
		return nil
	}
	return tlv(tagSequence, concat(tlv(tagOID, name), value))
}

// parseVarbindOIDs 由 varbinds SEQUENCE 內容取出各筆的 OID 名稱。
func parseVarbindOIDs(varbinds []byte) ([]string, error) {
	var oids []string
	rest := varbinds
	for len(rest) > 0 {
		tag, vb, next, err := readTLV(rest)
		if err != nil {
			return nil, err
		}
		rest = next
		if tag != tagSequence {
			continue
		}
		nameTag, name, _, err := readTLV(vb)
		if err != nil || nameTag != tagOID {
			return nil, errBER
		}
		oid, err := decodeOID(name)
		if err != nil {
			return nil, err
		}
		oids = append(oids, oid)
	}
	return oids, nil
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
