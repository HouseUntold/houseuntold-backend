package services

import (
	"fmt"
	"strings"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
)

// GuestInput represents an attendee provided during guest checkout.
type GuestInput struct {
	Name             string        `json:"name"`
	Age              int32         `json:"age"`
	Gender           db.GenderType `json:"gender"`
	IsPrimaryContact bool          `json:"is_primary_contact"`
}

// CheckoutPricing holds the authoritative server-calculated checkout totals.
type CheckoutPricing struct {
	TotalAmount    int32
	FnbCredit      int32
	First99Applied bool
	FreeShotPerk   bool
}

type GroupBookingInput struct {
	Couples    int32 `json:"couples"`
	StagMale   int32 `json:"stag_male"`
	StagFemale int32 `json:"stag_female"`
}

type GroupPricingInput struct {
	Group                GroupBookingInput
	First99Active        bool
	ConfirmedFemaleCount int64
	First99Limit         int64
	CouplePrice          int32
	CoupleFnb            int32
	MalePrice            int32
	MaleFnb              int32
	FemalePrice          int32
	FemaleFnb            int32
}

type EventPricingConfig struct {
	CouplePrice int32
	CoupleFnb   int32
	MalePrice   int32
	MaleFnb     int32
	FemalePrice int32
	FemaleFnb   int32
}

func EventPricingFromCategories(categories []db.TicketCategory) EventPricingConfig {
	config := EventPricingConfig{
		CouplePrice: 3299,
		CoupleFnb:   3000,
		MalePrice:   1999,
		MaleFnb:     1999,
		FemalePrice: 999,
		FemaleFnb:   999,
	}
	for _, cat := range categories {
		price, fnb := cat.BasePrice, cat.FnbCredit
		if price <= 0 || fnb <= 0 {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(cat.Name)) {
		case "COUPLE":
			config.CouplePrice, config.CoupleFnb = price, fnb
		case "STAG_MALE", "STAG":
			config.MalePrice, config.MaleFnb = price, fnb
		case "STAG_FEMALE":
			config.FemalePrice, config.FemaleFnb = price, fnb
		}
	}
	return config
}

type GroupPricing struct {
	TotalAmount          int32
	FnbCredit            int32
	FemalePrice          int32
	First99Applied       bool
	TequilaEligibleCount int32
	GuestCount           int32
}

// BarSettlement holds the zero-trust wallet deduction vs counter delta note calculation.
type BarSettlement struct {
	TotalBill      int64
	WalletDeducted int64
	DeltaToCollect int64
}

// ValidateCategoryGuests enforces entry counts and strict gender rules (Couple 1M+1F, Stag Male/Female).
func ValidateCategoryGuests(cat db.TicketCategory, guests []GuestInput) error {
	if len(guests) == 0 {
		return httpx.NewBadRequest("at least one guest is required", nil)
	}
	if int32(len(guests)) != cat.EntryCount {
		return httpx.NewBadRequest(
			fmt.Sprintf("this pass requires exactly %d guest(s), got %d", cat.EntryCount, len(guests)),
			nil,
		)
	}

	var maleCount, femaleCount int
	for i, g := range guests {
		trimmedName := strings.TrimSpace(g.Name)
		if trimmedName == "" || len(trimmedName) > 255 {
			return httpx.NewBadRequest(fmt.Sprintf("guest %d name is required (max 255 chars)", i+1), nil)
		}
		switch g.Gender {
		case db.GenderTypeMALE:
			maleCount++
		case db.GenderTypeFEMALE:
			femaleCount++
		case db.GenderTypeOTHER:
		default:
			return httpx.NewBadRequest(fmt.Sprintf("guest %d has invalid gender", i+1), nil)
		}
	}

	upperName := strings.ToUpper(strings.TrimSpace(cat.Name))
	if cat.IsGenderStrict || upperName == "COUPLE" {
		if cat.EntryCount != 2 || maleCount != 1 || femaleCount != 1 {
			return httpx.NewBadRequest("couple entry strictly requires 1 MALE and 1 FEMALE guest", nil)
		}
	}

	switch upperName {
	case "STAG_FEMALE":
		if femaleCount != int(cat.EntryCount) {
			return httpx.NewBadRequest("stag female pass requires FEMALE guest gender", nil)
		}
	case "STAG_MALE":
		if maleCount != int(cat.EntryCount) {
			return httpx.NewBadRequest("stag male pass requires MALE guest gender", nil)
		}
	}

	return nil
}

// CalculateCheckoutPricing applies GA tier pricing and the First 99 Female rule server-side.
func CalculateCheckoutPricing(cat db.TicketCategory, confirmedFemaleCount, first99Limit int64, first99Active bool) CheckoutPricing {
	upperName := strings.ToUpper(strings.TrimSpace(cat.Name))
	if upperName == "STAG_FEMALE" {
		if first99Active && confirmedFemaleCount < first99Limit {
			return CheckoutPricing{
				TotalAmount:    1,
				FnbCredit:      0,
				First99Applied: true,
				FreeShotPerk:   true,
			}
		}
		price, fnb := cat.BasePrice, cat.FnbCredit
		if price <= 0 {
			price = 999
		}
		if fnb <= 0 {
			fnb = 999
		}
		return CheckoutPricing{
			TotalAmount:    price,
			FnbCredit:      fnb,
			First99Applied: false,
			FreeShotPerk:   true,
		}
	}
	if upperName == "COUPLE" {
		price := cat.BasePrice
		if price <= 0 {
			price = 3299
		}
		fnb := cat.FnbCredit
		if fnb <= 0 {
			fnb = 3000
		}
		return CheckoutPricing{
			TotalAmount:    price,
			FnbCredit:      fnb,
			First99Applied: false,
			FreeShotPerk:   false,
		}
	}
	if strings.HasPrefix(upperName, "GROUP_") || upperName == "TABLE" {
		total := cat.BasePrice
		if total <= 0 && cat.EntryCount > 0 {
			total = int32(cat.EntryCount) * 1000
		}
		fnb := cat.FnbCredit
		if fnb <= 0 {
			fnb = total
		}
		return CheckoutPricing{
			TotalAmount:    total,
			FnbCredit:      fnb,
			First99Applied: false,
			FreeShotPerk:   false,
		}
	}

	price := cat.BasePrice
	if price <= 0 && (upperName == "STAG" || upperName == "STAG_MALE") {
		price = 1999
	}
	fnb := cat.FnbCredit
	if (upperName == "STAG" || upperName == "STAG_MALE") && fnb <= 0 {
		fnb = 1999
	}
	return CheckoutPricing{
		TotalAmount:    price,
		FnbCredit:      fnb,
		First99Applied: false,
		FreeShotPerk:   false,
	}
}

// CalculateGroupPricing prices the group builder server-side.
func CalculateGroupPricing(in GroupPricingInput) GroupPricing {
	couplePrice, coupleFnb := in.CouplePrice, in.CoupleFnb
	if couplePrice <= 0 {
		couplePrice = 3299
	}
	if coupleFnb <= 0 {
		coupleFnb = 3000
	}
	stagMalePrice, stagMaleFnb := in.MalePrice, in.MaleFnb
	if stagMalePrice <= 0 {
		stagMalePrice = 1999
	}
	if stagMaleFnb <= 0 {
		stagMaleFnb = 1999
	}
	femalePrice, femaleFnb := in.FemalePrice, in.FemaleFnb
	if femalePrice <= 0 {
		femalePrice = 999
	}
	if femaleFnb <= 0 {
		femaleFnb = 999
	}

	offerActive := in.First99Active && in.ConfirmedFemaleCount < in.First99Limit
	womenOnly := in.Group.Couples == 0 && in.Group.StagMale == 0 && in.Group.StagFemale > 0

	totalAmount := in.Group.Couples*couplePrice + in.Group.StagMale*stagMalePrice
	fnbCredit := in.Group.Couples*coupleFnb + in.Group.StagMale*stagMaleFnb
	femaleDisplayPrice := femalePrice

	if offerActive {
		if womenOnly {
			totalAmount = 1
			femaleDisplayPrice = 1
		} else {
			femaleDisplayPrice = 0
		}
	} else {
		totalAmount += in.Group.StagFemale * femalePrice
		fnbCredit += in.Group.StagFemale * femaleFnb
	}

	return GroupPricing{
		TotalAmount:          totalAmount,
		FnbCredit:            fnbCredit,
		FemalePrice:          femaleDisplayPrice,
		First99Applied:       offerActive && in.Group.StagFemale > 0,
		TequilaEligibleCount: in.Group.StagFemale,
		GuestCount:           in.Group.Couples*2 + in.Group.StagMale + in.Group.StagFemale,
	}
}

func BuildGroupGuests(group GroupBookingInput, primaryName string, age int32) []GuestInput {
	primaryName = strings.TrimSpace(primaryName)
	if primaryName == "" {
		primaryName = "Guest"
	}
	guests := make([]GuestInput, 0, group.Couples*2+group.StagMale+group.StagFemale)
	add := func(name string, gender db.GenderType) {
		guests = append(guests, GuestInput{
			Name:             name,
			Age:              age,
			Gender:           gender,
			IsPrimaryContact: len(guests) == 0,
		})
	}
	for i := int32(0); i < group.Couples; i++ {
		add(primaryName, db.GenderTypeMALE)
		add(primaryName, db.GenderTypeFEMALE)
	}
	for i := int32(0); i < group.StagMale; i++ {
		add(primaryName, db.GenderTypeMALE)
	}
	for i := int32(0); i < group.StagFemale; i++ {
		add(primaryName, db.GenderTypeFEMALE)
	}
	return guests
}

// CalculateBarSettlement splits a server-calculated bar bill across F&B wallet balance and counter delta reference.
func CalculateBarSettlement(totalBill, walletBalance int64) BarSettlement {
	if totalBill <= 0 {
		return BarSettlement{}
	}
	if walletBalance <= 0 {
		return BarSettlement{
			TotalBill:      totalBill,
			WalletDeducted: 0,
			DeltaToCollect: totalBill,
		}
	}
	if walletBalance >= totalBill {
		return BarSettlement{
			TotalBill:      totalBill,
			WalletDeducted: totalBill,
			DeltaToCollect: 0,
		}
	}
	return BarSettlement{
		TotalBill:      totalBill,
		WalletDeducted: walletBalance,
		DeltaToCollect: totalBill - walletBalance,
	}
}

// PrimaryGuestName selects the guest marked IsPrimaryContact, falling back to the first guest or "Guest".
func PrimaryGuestName(guests []db.TicketGuest) string {
	for _, g := range guests {
		if g.IsPrimaryContact && strings.TrimSpace(g.Name) != "" {
			return strings.TrimSpace(g.Name)
		}
	}
	if len(guests) > 0 && strings.TrimSpace(guests[0].Name) != "" {
		return strings.TrimSpace(guests[0].Name)
	}
	return "Guest"
}
