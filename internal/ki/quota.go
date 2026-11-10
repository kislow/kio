package ki

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultQuotaCooldown is how long a provider is skipped when its output
// says it is over a limit but gives no reset time ki can read.
const DefaultQuotaCooldown = 60 * time.Minute

// limitWording is what an agent prints when the account is over a limit:
// agy's quota and RESOURCE_EXHAUSTED errors, Claude Code's "hit your limit" /
// "usage limit reached", Cursor's "usage limit", and plain HTTP 429s.
var limitWording = regexp.MustCompile(`(?i)quota|resource_exhausted|\b429\b|usage limit|(hit|reached) (your|the|its) [a-z0-9 -]{0,20}limit|limit reached|rate[ _-]?limit|too many requests`)

var (
	resetsIn = regexp.MustCompile(`(?i)\bresets?\s+in\s+`)
	durPart  = regexp.MustCompile(`^(?:\s|,|and)*(\d+(?:\.\d+)?)\s*([a-z]+)`)
)

// resetsAt reads "resets at 7pm", "reset on Nov 9 at 19:00 (Asia/Dubai)",
// "resets 7:30 PM UTC": an optional date, a time that has am/pm or a colon,
// and an optional zone.
var resetsAt = regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+|on\s+|by\s+)?` +
	`(?:(?:(\d{4}-\d{2}-\d{2})|(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+(\d{1,2})(?:st|nd|rd|th)?)[\s,]*(?:at\s+)?)?` +
	`(\d{1,2})(?::(\d{2}))?\s*(?:([ap])\.?m\b\.?)?` +
	`(?:\s*\(?([a-z]+/[a-z_]+(?:/[a-z_]+)?|utc|gmt|z|[+-]\d{2}:?\d{2}|[ecmp][sd]t)\b\)?)?`)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// usZones are the fixed offsets of the US abbreviations agents print.
var usZones = map[string]int{"est": -5, "edt": -4, "cst": -6, "cdt": -5, "mst": -7, "mdt": -6, "pst": -8, "pdt": -7}

// QuotaFromText is quotaDuration for text outside the agent's JSON, such as
// what it printed to stderr before dying.
func QuotaFromText(text string, now time.Time) time.Duration { return quotaDuration(text, now) }

// quotaDuration is how long a provider will stay over its limit, 0 when the
// message does not say it is over one. The reset is read as a duration
// ("resets in 2h30m") or a clock time ("resets at 7pm", in the zone named, else
// now's); a limit with no reset ki can read cools down for DefaultQuotaCooldown.
func quotaDuration(message string, now time.Time) time.Duration {
	if !limitWording.MatchString(message) {
		return 0
	}
	if d := relativeReset(message); d > 0 {
		return d
	}
	if d := absoluteReset(message, now); d > 0 {
		return d
	}
	return DefaultQuotaCooldown
}

func relativeReset(message string) time.Duration {
	loc := resetsIn.FindStringIndex(message)
	if loc == nil {
		return 0
	}
	rest := message[loc[1]:]
	var total time.Duration
	for {
		m := durPart.FindStringSubmatch(rest)
		if m == nil {
			return total
		}
		n, err := strconv.ParseFloat(m[1], 64)
		unit := durationUnit(m[2])
		if err != nil || unit == 0 {
			return total
		}
		total += time.Duration(n * float64(unit))
		rest = rest[len(m[0]):]
	}
}

func durationUnit(word string) time.Duration {
	switch strings.ToLower(word) {
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute
	case "s", "sec", "secs", "second", "seconds":
		return time.Second
	}
	return 0
}

func absoluteReset(message string, now time.Time) time.Duration {
	m := resetsAt.FindStringSubmatch(message)
	if m == nil {
		return 0
	}
	iso, mon, day, hh, mm, ap, zone := m[1], strings.ToLower(m[2]), m[3], m[4], m[5], m[6], m[7]
	if mm == "" && ap == "" {
		return 0 // a bare number is not a time of day
	}
	hour, _ := strconv.Atoi(hh)
	minute, _ := strconv.Atoi(mm)
	switch {
	case ap != "":
		if hour < 1 || hour > 12 {
			return 0
		}
		hour %= 12
		if strings.EqualFold(ap, "p") {
			hour += 12
		}
	case hour > 23:
		return 0
	}
	if minute > 59 {
		return 0
	}
	loc := resetZone(zone, now.Location())
	local := now.In(loc)
	y, mo, d := local.Date()
	dated := false
	switch {
	case iso != "":
		t, err := time.ParseInLocation("2006-01-02", iso, loc)
		if err != nil {
			return 0
		}
		y, mo, d = t.Date()
		dated = true
	case mon != "":
		n, _ := strconv.Atoi(day)
		mo, d, dated = months[mon[:3]], n, true
	}
	at := time.Date(y, mo, d, hour, minute, 0, 0, loc)
	switch {
	case at.After(now):
	case dated && iso == "":
		at = at.AddDate(1, 0, 0) // "Jan 3" said in December is next year's
	case !dated:
		at = at.AddDate(0, 0, 1)
	}
	if !at.After(now) {
		return 0
	}
	return at.Sub(now)
}

// resetZone is the zone a reset time is written in; no zone, or one ki cannot
// resolve, means the machine's.
func resetZone(zone string, fallback *time.Location) *time.Location {
	z := strings.ToLower(zone)
	switch {
	case z == "":
		return fallback
	case z == "utc" || z == "gmt" || z == "z":
		return time.UTC
	case z[0] == '+' || z[0] == '-':
		digits := strings.ReplaceAll(z[1:], ":", "")
		h, _ := strconv.Atoi(digits[:2])
		m, _ := strconv.Atoi(digits[2:])
		secs := h*3600 + m*60
		if z[0] == '-' {
			secs = -secs
		}
		return time.FixedZone(zone, secs)
	}
	if h, ok := usZones[z]; ok {
		return time.FixedZone(strings.ToUpper(z), h*3600)
	}
	if loc, err := time.LoadLocation(zone); err == nil {
		return loc
	}
	return fallback
}
