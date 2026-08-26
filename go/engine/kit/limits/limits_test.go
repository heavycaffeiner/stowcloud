package limits

import (
	"errors"
	"testing"
)

func TestExceedWrapsErrTooLarge(t *testing.T) {
	err := Exceed("RequestBody", RequestBody, RequestBody+1)

	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("errors.Is(err, ErrTooLarge) = false, want true")
	}

	var exceeded *Exceeded
	if !errors.As(err, &exceeded) {
		t.Fatalf("errors.As(err, *Exceeded) failed")
	}
	if exceeded.Limit != "RequestBody" {
		t.Errorf("Limit = %q, want %q", exceeded.Limit, "RequestBody")
	}
	if exceeded.Bound != RequestBody {
		t.Errorf("Bound = %d, want %d", exceeded.Bound, int64(RequestBody))
	}
	if exceeded.Got != RequestBody+1 {
		t.Errorf("Got = %d, want %d", exceeded.Got, int64(RequestBody+1))
	}
}

func TestExceededErrorMessageCarriesTheValues(t *testing.T) {
	err := Exceed("SearchQueryBytes", 10, 20)

	const want = "SearchQueryBytes: 20 exceeds the limit of 10"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestExceededIsRejectsOtherTargets(t *testing.T) {
	err := Exceed("PathBytes", PathBytes, PathBytes+1)

	if errors.Is(err, errors.New("something else")) {
		t.Errorf("errors.Is matched an unrelated sentinel")
	}
}
