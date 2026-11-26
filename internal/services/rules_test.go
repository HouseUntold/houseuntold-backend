package services

import (
	"testing"

	"github.com/venu-live/venu-api/internal/db"
)

func TestCalculateCheckoutPricing_First99Rule(t *testing.T) {
	femaleTier := db.TicketCategory{
		Name:       "STAG_FEMALE",
		BasePrice:  999,
		FnbCredit:  999,
		EntryCount: 1,
	}

	p0 := CalculateCheckoutPricing(femaleTier, 0, 99, true)
	if p0.TotalAmount != 1 || p0.FnbCredit != 0 || !p0.First99Applied || !p0.FreeShotPerk {
		t.Fatalf("expected INR 1 convenience fee first-99 pricing at slot 0, got %+v", p0)
	}

	p98 := CalculateCheckoutPricing(femaleTier, 98, 99, true)
	if p98.TotalAmount != 1 || p98.FnbCredit != 0 || !p98.First99Applied {
		t.Fatalf("expected INR 1 pricing at 99th slot (count 98), got %+v", p98)
	}

	p99 := CalculateCheckoutPricing(femaleTier, 99, 99, true)
	if p99.TotalAmount != 999 || p99.FnbCredit != 999 || p99.First99Applied || !p99.FreeShotPerk {
		t.Fatalf("expected paid INR 999 pricing at 100th slot (count 99), got %+v", p99)
	}

	inactive := CalculateCheckoutPricing(femaleTier, 0, 99, false)
	if inactive.TotalAmount != 999 || inactive.FnbCredit != 999 || inactive.First99Applied {
		t.Fatalf("expected flat INR 999 when first-99 pricing is disabled, got %+v", inactive)
	}
}

func TestCalculateCheckoutPricing_TableDriven(t *testing.T) {
	femaleTier := db.TicketCategory{Name: "STAG_FEMALE", BasePrice: 1500, FnbCredit: 1000, EntryCount: 1}
	maleTier := db.TicketCategory{Name: "STAG_MALE", BasePrice: 2000, FnbCredit: 1000, EntryCount: 1}

	tests := []struct {
		name      string
		category  db.TicketCategory
		confirmed int64
		limit     int64
		active    bool
		want      CheckoutPricing
	}{
		{name: "female slot 1 first-99", category: femaleTier, confirmed: 0, limit: 99, active: true, want: CheckoutPricing{TotalAmount: 1, FnbCredit: 0, First99Applied: true, FreeShotPerk: true}},
		{name: "female slot 99 first-99", category: femaleTier, confirmed: 98, limit: 99, active: true, want: CheckoutPricing{TotalAmount: 1, FnbCredit: 0, First99Applied: true, FreeShotPerk: true}},
		{name: "female limit exhausted", category: femaleTier, confirmed: 99, limit: 99, active: true, want: CheckoutPricing{TotalAmount: 1500, FnbCredit: 1000, FreeShotPerk: true}},
		{name: "custom limit exhausted", category: femaleTier, confirmed: 25, limit: 25, active: true, want: CheckoutPricing{TotalAmount: 1500, FnbCredit: 1000, FreeShotPerk: true}},
		{name: "first-99 disabled", category: femaleTier, confirmed: 0, limit: 99, want: CheckoutPricing{TotalAmount: 1500, FnbCredit: 1000, FreeShotPerk: true}},
		{name: "male tier never free", category: maleTier, confirmed: 0, limit: 99, active: true, want: CheckoutPricing{TotalAmount: 2000, FnbCredit: 1000}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateCheckoutPricing(tt.category, tt.confirmed, tt.limit, tt.active); got != tt.want {
				t.Fatalf("CalculateCheckoutPricing() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateCategoryGuests_CoupleStrictRule(t *testing.T) {
	coupleTier := db.TicketCategory{
		Name:           "COUPLE",
		BasePrice:      3299,
		FnbCredit:      3000,
		EntryCount:     2,
		IsGenderStrict: true,
	}

	valid := []GuestInput{
		{Name: "Aarav", Age: 25, Gender: db.GenderTypeMALE},
		{Name: "Meera", Age: 24, Gender: db.GenderTypeFEMALE},
	}
	if err := ValidateCategoryGuests(coupleTier, valid); err != nil {
		t.Fatalf("expected valid 1M+1F couple to pass, got %v", err)
	}

	twoMales := []GuestInput{
		{Name: "Aarav", Age: 25, Gender: db.GenderTypeMALE},
		{Name: "Rohan", Age: 26, Gender: db.GenderTypeMALE},
	}
	if err := ValidateCategoryGuests(coupleTier, twoMales); err == nil {
		t.Fatalf("expected 2M couple to be rejected")
	}

	anyAge := []GuestInput{
		{Name: "Aarav", Age: 19, Gender: db.GenderTypeMALE},
		{Name: "Meera", Age: 22, Gender: db.GenderTypeFEMALE},
	}
	if err := ValidateCategoryGuests(coupleTier, anyAge); err != nil {
		t.Fatalf("expected age to be ignored by guest validation, got %v", err)
	}
}

func TestCalculateGroupPricing(t *testing.T) {
	tests := []struct {
		name string
		in   GroupPricingInput
		want GroupPricing
	}{
		{
			name: "women only offer active is flat one rupee",
			in:   GroupPricingInput{Group: GroupBookingInput{StagFemale: 5}, First99Active: true, First99Limit: 99},
			want: GroupPricing{TotalAmount: 1, FnbCredit: 0, FemalePrice: 1, First99Applied: true, TequilaEligibleCount: 5, GuestCount: 5},
		},
		{
			name: "mixed group offer active makes women free",
			in:   GroupPricingInput{Group: GroupBookingInput{Couples: 1, StagMale: 1, StagFemale: 2}, First99Active: true, First99Limit: 99},
			want: GroupPricing{TotalAmount: 5298, FnbCredit: 4999, FemalePrice: 0, First99Applied: true, TequilaEligibleCount: 2, GuestCount: 5},
		},
		{
			name: "women only offer inactive is per woman",
			in:   GroupPricingInput{Group: GroupBookingInput{StagFemale: 5}},
			want: GroupPricing{TotalAmount: 4995, FnbCredit: 4995, FemalePrice: 999, TequilaEligibleCount: 5, GuestCount: 5},
		},
		{
			name: "mixed group offer inactive loads female wallet",
			in:   GroupPricingInput{Group: GroupBookingInput{Couples: 1, StagMale: 1, StagFemale: 2}},
			want: GroupPricing{TotalAmount: 7296, FnbCredit: 6997, FemalePrice: 999, TequilaEligibleCount: 2, GuestCount: 5},
		},
		{
			name: "offer turns off at female limit",
			in:   GroupPricingInput{Group: GroupBookingInput{StagFemale: 2}, First99Active: true, ConfirmedFemaleCount: 99, First99Limit: 99},
			want: GroupPricing{TotalAmount: 1998, FnbCredit: 1998, FemalePrice: 999, TequilaEligibleCount: 2, GuestCount: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateGroupPricing(tt.in); got != tt.want {
				t.Fatalf("CalculateGroupPricing() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBuildGroupGuestsUsesPlainNames(t *testing.T) {
	guests := BuildGroupGuests(GroupBookingInput{Couples: 1, StagMale: 1, StagFemale: 1}, " Kumar Kartik ", 25)
	if len(guests) != 4 {
		t.Fatalf("expected 4 guests, got %d", len(guests))
	}
	wantGenders := []db.GenderType{db.GenderTypeMALE, db.GenderTypeFEMALE, db.GenderTypeMALE, db.GenderTypeFEMALE}
	for i, guest := range guests {
		if guest.Name != "Kumar Kartik" {
			t.Fatalf("guest %d name = %q, want plain name", i, guest.Name)
		}
		if guest.Gender != wantGenders[i] {
			t.Fatalf("guest %d gender = %q, want %q", i, guest.Gender, wantGenders[i])
		}
		if guest.IsPrimaryContact != (i == 0) {
			t.Fatalf("guest %d primary flag = %v", i, guest.IsPrimaryContact)
		}
	}
}

func TestCalculateBarSettlement(t *testing.T) {
	s1 := CalculateBarSettlement(800, 1999)
	if s1.WalletDeducted != 800 || s1.DeltaToCollect != 0 {
		t.Fatalf("expected full wallet cover, got %+v", s1)
	}

	s2 := CalculateBarSettlement(1500, 999)
	if s2.WalletDeducted != 999 || s2.DeltaToCollect != 501 {
		t.Fatalf("expected 999 wallet + 501 counter delta, got %+v", s2)
	}

	s3 := CalculateBarSettlement(600, 0)
	if s3.WalletDeducted != 0 || s3.DeltaToCollect != 600 {
		t.Fatalf("expected full counter delta when wallet is empty, got %+v", s3)
	}
}
