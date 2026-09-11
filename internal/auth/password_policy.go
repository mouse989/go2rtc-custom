package auth

import (
	"errors"
	"unicode"
)

// passwordPolicyMsg is shown to the user (via HTTP error body) whenever a
// password fails validatePassword — kept as one exported-looking constant so
// the wording only has to be written once.
const passwordPolicyMsg = "password must be at least 8 characters and include a number and a special character"

// validatePassword enforces the site's password policy: at least 8
// characters, containing at least one digit and one special (non-letter,
// non-digit) character. Applied everywhere a password is set — user
// creation, admin-initiated reset, and self-service change.
func validatePassword(pw string) error {
	if len(pw) < 8 {
		return errors.New(passwordPolicyMsg)
	}
	var hasDigit, hasSpecial bool
	for _, r := range pw {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case !unicode.IsLetter(r):
			hasSpecial = true
		}
	}
	if !hasDigit || !hasSpecial {
		return errors.New(passwordPolicyMsg)
	}
	return nil
}
