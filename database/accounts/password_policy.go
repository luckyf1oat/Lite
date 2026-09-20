package accounts

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

var (
	ErrPasswordTooShort = errors.New("Password must be at least 8 characters long")
	ErrPasswordTooWeak  = errors.New("Password must contain uppercase, lowercase, and a number")
)

func ValidateNewPassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return ErrPasswordTooShort
	}
	var hasLower, hasUpper, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLower || !hasUpper || !hasDigit {
		return ErrPasswordTooWeak
	}
	return nil
}
