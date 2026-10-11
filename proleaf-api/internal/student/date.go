package student

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// DateLayout is the only date format the API accepts or emits. A session date
// and a date of birth are calendar dates, not instants: encoding them as
// RFC3339 timestamps invites a timezone to shift one across a day boundary.
const DateLayout = "2006-01-02"

// Date is a calendar date that marshals as "2006-01-02".
type Date time.Time

func (d Date) Time() time.Time { return time.Time(d) }

func (d Date) String() string { return d.Time().Format(DateLayout) }

func (d Date) IsZero() bool { return d.Time().IsZero() }

func ParseDate(s string) (Date, error) {
	t, err := time.ParseInLocation(DateLayout, s, time.UTC)
	if err != nil {
		return Date{}, errors.New("date must be formatted YYYY-MM-DD")
	}
	return Date(t), nil
}

// NewDate truncates an instant to its UTC calendar date.
func NewDate(t time.Time) Date {
	t = t.UTC()
	return Date(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
}

func (d Date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

func (d *Date) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*d = Date{}
		return nil
	}
	parsed, err := ParseDate(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Before reports whether d falls before other on the calendar.
func (d Date) Before(other Date) bool { return d.Time().Before(other.Time()) }

// After reports whether d falls after other on the calendar.
func (d Date) After(other Date) bool { return d.Time().After(other.Time()) }
