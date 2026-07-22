// 本檔為極簡 BER（ASN.1 Basic Encoding Rules）編解碼工具，僅供本套件的 mock SNMP
// responder（mocksnmp.go）使用——正式的 SNMP 查詢走 gosnmp，不經此處。
//
// 只實作 SNMP v2c GetRequest/GetNextRequest/GetBulkRequest 與 GetResponse 會用到的最小子集：
// SEQUENCE、INTEGER、OCTET STRING、OBJECT IDENTIFIER、Counter32 與三個例外標記
// （noSuchObject/noSuchInstance/endOfMibView）。刻意不追求完整 ASN.1 相容性。
package printer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errBER 表示位元組串不是本子集可解析的 BER 結構（截斷、長度欄位過長等）。
var errBER = errors.New("printer: malformed BER")

// BER/SNMP tag（僅列本套件用到者；數值同 gosnmp.Asn1BER）。
const (
	tagInteger      byte = 0x02
	tagOctetString  byte = 0x04
	tagNull         byte = 0x05
	tagOID          byte = 0x06
	tagSequence     byte = 0x30
	tagCounter32    byte = 0x41
	tagGetRequest   byte = 0xa0
	tagGetNext      byte = 0xa1
	tagGetResponse  byte = 0xa2
	tagGetBulk      byte = 0xa5
	tagNoSuchObject byte = 0x80
	tagNoSuchInst   byte = 0x81
	tagEndOfMibView byte = 0x82
)

// readTLV 由 b 開頭切下一個 TLV，回傳其 tag、內容與剩餘位元組。
func readTLV(b []byte) (tag byte, content, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, errBER
	}
	tag = b[0]
	length := int(b[1])
	off := 2
	if length&0x80 != 0 { // 長格式：低 7 位為「長度的位元組數」
		n := length & 0x7f
		if n == 0 || n > 4 || len(b) < 2+n {
			return 0, nil, nil, errBER
		}
		length = 0
		for _, c := range b[2 : 2+n] {
			length = length<<8 | int(c)
		}
		off = 2 + n
	}
	if length < 0 || len(b) < off+length {
		return 0, nil, nil, errBER
	}
	return tag, b[off : off+length], b[off+length:], nil
}

// tlv 以 tag 包裝內容為一個完整 TLV（自動選短／長長度格式）。
func tlv(tag byte, content []byte) []byte {
	out := make([]byte, 0, len(content)+6)
	out = append(out, tag)
	out = append(out, encodeLength(len(content))...)
	return append(out, content...)
}

func encodeLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var raw []byte
	for v := n; v > 0; v >>= 8 {
		raw = append([]byte{byte(v)}, raw...)
	}
	return append([]byte{0x80 | byte(len(raw))}, raw...)
}

// decodeInt 解析 BER INTEGER 內容（二補數大端）。
func decodeInt(content []byte) (int64, error) {
	if len(content) == 0 || len(content) > 8 {
		return 0, errBER
	}
	v := int64(0)
	if content[0]&0x80 != 0 {
		v = -1 // 負數：以全 1 起始做二補數延伸
	}
	for _, b := range content {
		v = v<<8 | int64(b)
	}
	return v, nil
}

// encodeUint 以「非負整數」語意編出內容位元組：最小長度，最高位為 1 時補前導 0x00，
// 避免被解讀為負數（Counter32 等無號型別亦沿用 INTEGER 的內容編碼）。
func encodeUint(v uint64) []byte {
	if v == 0 {
		return []byte{0x00}
	}
	var raw []byte
	for x := v; x > 0; x >>= 8 {
		raw = append([]byte{byte(x)}, raw...)
	}
	if raw[0]&0x80 != 0 {
		raw = append([]byte{0x00}, raw...)
	}
	return raw
}

// decodeOID 將 BER OID 內容還原為點分字串（如 "1.3.6.1.2.1.43.10.2.1.4.1.1"）。
func decodeOID(content []byte) (string, error) {
	if len(content) == 0 {
		return "", errBER
	}
	// 首位元組編碼前兩節：40*first + second。
	parts := []uint64{uint64(content[0]) / 40, uint64(content[0]) % 40}
	var acc uint64
	pending := false
	for _, b := range content[1:] {
		acc = acc<<7 | uint64(b&0x7f)
		pending = true
		if b&0x80 == 0 {
			parts = append(parts, acc)
			acc, pending = 0, false
		}
	}
	if pending {
		return "", errBER // 最後一節未以「續接位為 0」收尾
	}
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(strconv.FormatUint(p, 10))
	}
	return sb.String(), nil
}

// encodeOID 將點分字串編為 BER OID TLV 的內容位元組。
func encodeOID(oid string) ([]byte, error) {
	nums, err := parseOID(oid)
	if err != nil {
		return nil, err
	}
	if len(nums) < 2 {
		return nil, fmt.Errorf("printer: OID %q too short", oid)
	}
	out := []byte{byte(nums[0]*40 + nums[1])}
	for _, n := range nums[2:] {
		out = append(out, base128(n)...)
	}
	return out, nil
}

// base128 以 7 位一組（除末組外最高位設 1）編出多位元組整數。
func base128(v uint64) []byte {
	if v == 0 {
		return []byte{0x00}
	}
	var raw []byte
	for x := v; x > 0; x >>= 7 {
		raw = append([]byte{byte(x & 0x7f)}, raw...)
	}
	for i := 0; i < len(raw)-1; i++ {
		raw[i] |= 0x80
	}
	return raw
}

// parseOID 將點分字串切為各節數值（容忍前導 "."）。
func parseOID(oid string) ([]uint64, error) {
	s := strings.TrimPrefix(strings.TrimSpace(oid), ".")
	if s == "" {
		return nil, fmt.Errorf("printer: empty OID")
	}
	fields := strings.Split(s, ".")
	nums := make([]uint64, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("printer: invalid OID %q: %w", oid, err)
		}
		nums = append(nums, n)
	}
	return nums, nil
}

// compareOID 以「逐節數值」比較兩個 OID（非字串字典序——後者會把 .10 排在 .2 前面）。
// 回傳 -1／0／1。無法解析者視為最大，排到尾端。
func compareOID(a, b string) int {
	na, erra := parseOID(a)
	nb, errb := parseOID(b)
	switch {
	case erra != nil && errb != nil:
		return strings.Compare(a, b)
	case erra != nil:
		return 1
	case errb != nil:
		return -1
	}
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(na) < len(nb):
		return -1
	case len(na) > len(nb):
		return 1
	}
	return 0
}
