// Package signup shows what `validate:"required"` actually means, which is
// not what it reads like.
//
// required means "not the zero value", so an int of 0 and a bool of false
// fail it. On a signup form that is usually right for Email and usually
// wrong for Age and for any flag whose legitimate value is false: the
// request is rejected with "Age is required" when Age was supplied.
//
// The fix is a pointer plus required, which then means "present": a
// *bool pointing at false passes, and only nil fails. Measured, because
// the two readings of the same tag differ only by one asterisk.
package signup

import "github.com/go-playground/validator/v10"

type Value struct {
	Email    string `validate:"required,email"`
	Age      int    `validate:"required,gte=18"`
	Accepted bool   `validate:"required"`
}

type Pointer struct {
	Accepted *bool `validate:"required"`
	Count    *int  `validate:"required"`
	Optional *int  `validate:"omitempty,gte=1"`
}

func New() *validator.Validate {
	return validator.New(validator.WithRequiredStructEnabled())
}

// Failures returns "Field:tag" for each violation, in declaration order.
func Failures(v *validator.Validate, s any) []string {
	err := v.Struct(s)
	if err == nil {
		return nil
	}
	var out []string
	for _, fe := range err.(validator.ValidationErrors) {
		out = append(out, fe.Field()+":"+fe.Tag())
	}
	return out
}
