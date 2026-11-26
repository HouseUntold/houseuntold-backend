package db

func StaffAccountFromGetStaffByUsername(row GetStaffByUsernameRow) StaffAccount {
	return StaffAccount{
		ID:           row.ID,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		Role:         row.Role,
		IsActive:     row.IsActive,
		TokenVersion: row.TokenVersion,
		CreatedAt:    row.CreatedAt,
	}
}

func StaffAccountFromGetStaffByID(row GetStaffByIDRow) StaffAccount {
	return StaffAccount{
		ID:           row.ID,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		Role:         row.Role,
		IsActive:     row.IsActive,
		TokenVersion: row.TokenVersion,
		CreatedAt:    row.CreatedAt,
	}
}

func StaffAccountFromCreateStaff(row CreateStaffRow) StaffAccount {
	return StaffAccount{
		ID:           row.ID,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		Role:         row.Role,
		IsActive:     row.IsActive,
		TokenVersion: row.TokenVersion,
		CreatedAt:    row.CreatedAt,
	}
}

func TicketGuestsFromListRows(rows []ListGuestsByTicketIDRow) []TicketGuest {
	guests := make([]TicketGuest, 0, len(rows))
	for _, row := range rows {
		guests = append(guests, TicketGuest{
			ID:               row.ID,
			TicketID:         row.TicketID,
			Name:             row.Name,
			Age:              row.Age,
			Gender:           row.Gender,
			IsPrimaryContact: row.IsPrimaryContact,
			FreeShotEligible: row.FreeShotEligible,
			FreeShotRedeemed: row.FreeShotRedeemed,
		})
	}
	return guests
}
