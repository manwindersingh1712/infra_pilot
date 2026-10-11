package auth

import "golang.org/x/crypto/bcrypt"

const bcryptCost = 10 // same as the Node API's SALT_ROUNDS

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

// CheckPassword also accepts the $2b$ hashes written by Node's bcrypt.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
