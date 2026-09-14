//go:build darwin

package platform

// macOS 金鑰庫實作（v12 §1 / §4.4.2；docs/Eco-Agent_後端串接改動清單.md §4）：透過 cgo 呼叫
// Security.framework Keychain Services（SecItemAdd/SecItemCopyMatching/SecItemUpdate/
// SecItemDelete），以 kSecClassGenericPassword 項目存放，service 固定 "eco-agent"、
// account 即呼叫端的 key（例如 "eco-agent.refresh_token"）。系統鑰匙圈本身即已加密並綁定
// 目前使用者登入，不需自行加解密。
//
// NOTE：本檔僅能在 macOS（darwin）以 cgo 建置與執行；非 darwin 平台一律略過（build tag）。

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

// 以下三個輔助函式把「建 query dictionary → 呼叫 SecItem* → 收 CFTypeRef 結果」的樣板
// 收在 C 側，Go 側只需處理 CString 轉換與 OSStatus，降低 CFDictionary 操作在 cgo 邊界
// 來回的複雜度與出錯機會。

static OSStatus eco_keychain_set(const char *service, const char *account, const void *data, int dataLen) {
    CFStringRef cfService = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    CFDataRef cfData = CFDataCreate(NULL, (const UInt8 *)data, dataLen);

    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, cfService);
    CFDictionarySetValue(query, kSecAttrAccount, cfAccount);

    CFMutableDictionaryRef attrsToUpdate = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(attrsToUpdate, kSecValueData, cfData);

    OSStatus status = SecItemUpdate(query, attrsToUpdate);
    if (status == errSecItemNotFound) {
        CFDictionarySetValue(query, kSecValueData, cfData);
        status = SecItemAdd(query, NULL);
    }

    CFRelease(query);
    CFRelease(attrsToUpdate);
    CFRelease(cfService);
    CFRelease(cfAccount);
    CFRelease(cfData);
    return status;
}

static OSStatus eco_keychain_get(const char *service, const char *account, void **outData, int *outLen) {
    CFStringRef cfService = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);

    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, cfService);
    CFDictionarySetValue(query, kSecAttrAccount, cfAccount);
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);

    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    CFRelease(query);
    CFRelease(cfService);
    CFRelease(cfAccount);

    if (status != errSecSuccess) {
        return status;
    }
    CFDataRef data = (CFDataRef)result;
    CFIndex len = CFDataGetLength(data);
    void *buf = malloc(len > 0 ? len : 1);
    CFDataGetBytes(data, CFRangeMake(0, len), (UInt8 *)buf);
    *outData = buf;
    *outLen = (int)len;
    CFRelease(result);
    return status;
}

static OSStatus eco_keychain_delete(const char *service, const char *account) {
    CFStringRef cfService = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);

    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, cfService);
    CFDictionarySetValue(query, kSecAttrAccount, cfAccount);

    OSStatus status = SecItemDelete(query);

    CFRelease(query);
    CFRelease(cfService);
    CFRelease(cfAccount);
    return status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// keychainService 是所有 Eco-Agent 鑰匙圈項目共用的 kSecAttrService；kSecAttrAccount
// 用呼叫端傳入的 key 區分不同項目（例如 "eco-agent.refresh_token"）。
const keychainService = "eco-agent"

// errSecItemNotFound 對應 Security.framework 的「找不到項目」狀態碼，寫死數值以避免
// 額外從 cgo 側匯出常數；數值出自 <Security/SecBase.h>，跨 macOS 版本穩定。
const errSecItemNotFound = C.OSStatus(-25300)

type darwinKeychain struct{}

// NewOSKeychain 回傳 macOS Keychain Services 實作。
func NewOSKeychain() Keychain { return darwinKeychain{} }

// Get 實作 Keychain。
func (darwinKeychain) Get(key string) (string, error) {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(key)
	defer C.free(unsafe.Pointer(cAccount))

	var outData unsafe.Pointer
	var outLen C.int
	status := C.eco_keychain_get(cService, cAccount, &outData, &outLen)
	if status == errSecItemNotFound {
		return "", ErrKeychainNotFound
	}
	if status != 0 {
		return "", fmt.Errorf("platform: keychain get %q failed (OSStatus %d)", key, int(status))
	}
	defer C.free(outData)
	return C.GoStringN((*C.char)(outData), outLen), nil
}

// Set 實作 Keychain。
func (darwinKeychain) Set(key, value string) error {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(key)
	defer C.free(unsafe.Pointer(cAccount))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))

	status := C.eco_keychain_set(cService, cAccount, unsafe.Pointer(cValue), C.int(len(value)))
	if status != 0 {
		return fmt.Errorf("platform: keychain set %q failed (OSStatus %d)", key, int(status))
	}
	return nil
}

// Delete 實作 Keychain（冪等：項目不存在視為成功）。
func (darwinKeychain) Delete(key string) error {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(key)
	defer C.free(unsafe.Pointer(cAccount))

	status := C.eco_keychain_delete(cService, cAccount)
	if status != 0 && status != errSecItemNotFound {
		return fmt.Errorf("platform: keychain delete %q failed (OSStatus %d)", key, int(status))
	}
	return nil
}

// 確保 darwinKeychain 滿足 Keychain 介面。
var _ Keychain = darwinKeychain{}
