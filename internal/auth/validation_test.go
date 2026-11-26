package auth

import "testing"

func TestNormalizeIndianMobile(t *testing.T) {
	valid := map[string]string{
		"9876543211":        "+919876543211",
		"+91 98765-43211":   "+919876543211",
		"91 (98765) 432.11": "+919876543211",
		"09876543211":       "+919876543211",
	}
	for input, expected := range valid {
		got, err := NormalizeIndianMobile(input)
		if err != nil || got != expected {
			t.Fatalf("NormalizeIndianMobile(%q) = %q, %v; want %q", input, got, err, expected)
		}
	}
	for _, input := range []string{"987654321", "19876543211", "0876543211", "987654321a", "9876543210", "6789012345", "1111111111"} {
		if _, err := NormalizeIndianMobile(input); err == nil {
			t.Fatalf("expected mobile %q to be rejected", input)
		}
	}
}

func TestNormalizeUPIUTR(t *testing.T) {
	got, err := NormalizeUPIUTR(" 4321 9876 5432 ")
	if err != nil || got != "432198765432" {
		t.Fatalf("NormalizeUPIUTR returned %q, %v", got, err)
	}
	for _, input := range []string{"123", "1234567890123", "43219876543A", "000000000000", "123456789012", "987654321098"} {
		if _, err := NormalizeUPIUTR(input); err == nil {
			t.Fatalf("expected UTR %q to be rejected", input)
		}
	}
}
