package auth

import (
	"errors"
	"strings"
)

var mobilePixels = map[string]bool{"9876543210": true, "6789012345": true}

func NormalizeIndianMobile(raw string) (string, error) {
	cleaned := strings.NewReplacer(" ", "", "-", "", ".", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(cleaned, "+91") {
		cleaned = strings.TrimPrefix(cleaned, "+91")
	} else if len(cleaned) == 12 && strings.HasPrefix(cleaned, "91") {
		cleaned = strings.TrimPrefix(cleaned, "91")
	} else if len(cleaned) == 11 && strings.HasPrefix(cleaned, "0") {
		cleaned = strings.TrimPrefix(cleaned, "0")
	}
	if !allDigits(cleaned) {
		return "", errors.New("mobile number must contain digits only")
	}
	if len(cleaned) != 10 {
		return "", errors.New("mobile number must be 10 digits after the country code")
	}
	if cleaned[0] < '6' || cleaned[0] > '9' {
		return "", errors.New("mobile number must start with 6, 7, 8 or 9")
	}
	if allSameDigits(cleaned) || mobilePixels[cleaned] {
		return "", errors.New("this mobile number is not valid")
	}
	return "+91" + cleaned, nil
}

func NormalizeUPIUTR(raw string) (string, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(raw), " ", "")
	if !allDigits(cleaned) {
		return "", errors.New("UTR must contain digits only")
	}
	if len(cleaned) != 12 {
		return "", errors.New("UTR must be exactly 12 digits")
	}
	if allSameDigits(cleaned) || isSequentialDigits(cleaned) {
		return "", errors.New("this UTR is a dummy pattern and cannot be used")
	}
	return cleaned, nil
}

func IsValidUPITransactionID(txnID string) bool {
	_, err := NormalizeUPIUTR(txnID)
	return err == nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allSameDigits(value string) bool {
	for i := 1; i < len(value); i++ {
		if value[i] != value[0] {
			return false
		}
	}
	return true
}

func isSequentialDigits(value string) bool {
	ascending, descending := true, true
	for i := 1; i < len(value); i++ {
		previous := int(value[i-1] - '0')
		current := int(value[i] - '0')
		if current != (previous+1)%10 {
			ascending = false
		}
		if current != (previous+9)%10 {
			descending = false
		}
	}
	return ascending || descending
}
