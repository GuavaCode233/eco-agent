package printer

import "testing"

// OID 編碼→解碼往返（含首位元組的 40*a+b 合併與多位元組節）。
func TestOIDRoundTrip(t *testing.T) {
	oids := []string{
		"1.3.6.1.2.1.43.10.2.1.4",
		DefaultPageCounterOID,
		"1.3.6.1.2.1.43.10.2.1.4.1.128",   // 需兩位元組 base-128
		"1.3.6.1.4.1.11.2.3.9.4.2.1.1.16", // 廠商私有樹（HP）
	}
	for _, oid := range oids {
		enc, err := encodeOID(oid)
		if err != nil {
			t.Fatalf("encodeOID(%s): %v", oid, err)
		}
		got, err := decodeOID(enc)
		if err != nil {
			t.Fatalf("decodeOID(%s): %v", oid, err)
		}
		if got != oid {
			t.Errorf("round trip = %q, want %q", got, oid)
		}
	}
}

// OID 比較須逐節取數值，而非字串字典序（"…1.10" > "…1.2"）。
func TestCompareOIDNumericOrder(t *testing.T) {
	a := OIDPageCounterColumn + ".1.2"
	b := OIDPageCounterColumn + ".1.10"
	if compareOID(a, b) >= 0 {
		t.Errorf("compareOID(%s, %s) 應為負（.1.2 在前）", a, b)
	}
	if compareOID(b, a) <= 0 {
		t.Errorf("compareOID(%s, %s) 應為正", b, a)
	}
	if compareOID(a, a) != 0 {
		t.Errorf("compareOID 相同 OID 應為 0")
	}
	// 前綴較短者在前。
	if compareOID(OIDPageCounterColumn, a) >= 0 {
		t.Errorf("欄 OID 應排在其 instance 之前")
	}
}

// 長度欄位的短／長格式與無號整數編碼。
func TestEncodeLengthAndUint(t *testing.T) {
	if got := encodeLength(5); len(got) != 1 || got[0] != 5 {
		t.Errorf("encodeLength(5) = %v, want [5]", got)
	}
	if got := encodeLength(200); len(got) != 2 || got[0] != 0x81 || got[1] != 200 {
		t.Errorf("encodeLength(200) = %v, want [0x81 200]", got)
	}
	// 最高位為 1 時須補前導 0x00，否則會被解讀為負數。
	got := encodeUint(0xFF)
	if len(got) != 2 || got[0] != 0x00 || got[1] != 0xFF {
		t.Errorf("encodeUint(0xFF) = %v, want [0x00 0xFF]", got)
	}
	if got := encodeUint(0); len(got) != 1 || got[0] != 0 {
		t.Errorf("encodeUint(0) = %v, want [0]", got)
	}
}

func TestReadTLVMalformed(t *testing.T) {
	cases := [][]byte{
		{},                          // 空
		{0x30},                      // 只有 tag
		{0x30, 0x05, 0x01},          // 長度大於實際內容
		{0x30, 0x85, 1, 2, 3, 4, 5}, // 長度欄位超過 4 位元組
	}
	for i, c := range cases {
		if _, _, _, err := readTLV(c); err == nil {
			t.Errorf("case %d: 應回錯誤，實際回 nil", i)
		}
	}
}

func TestDecodeIntTwosComplement(t *testing.T) {
	tests := []struct {
		in   []byte
		want int64
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x7F}, 127},
		{[]byte{0x00, 0xFF}, 255},
		{[]byte{0xFF}, -1},
	}
	for _, tt := range tests {
		got, err := decodeInt(tt.in)
		if err != nil {
			t.Fatalf("decodeInt(%v): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("decodeInt(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
