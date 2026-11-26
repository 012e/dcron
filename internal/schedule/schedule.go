package schedule

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Kind identifies how a schedule triggers.
type Kind int

const (
	KindUnknown Kind = iota
	KindCron
	KindInterval
	KindReboot
)

func (k Kind) String() string {
	switch k {
	case KindCron:
		return "cron"
	case KindInterval:
		return "interval"
	case KindReboot:
		return "reboot"
	default:
		return "unknown"
	}
}

// Schedule is the parsed, normalized representation of a job's schedule.
type Schedule struct {
	Raw         string
	Kind        Kind
	CronExpr    string
	Interval    time.Duration
	RebootDelay time.Duration
}

// Summary returns a concise normalized representation suitable for CLI tables.
func (s Schedule) Summary() string {
	switch s.Kind {
	case KindCron:
		return s.CronExpr
	case KindInterval:
		return fmt.Sprintf("every %s", s.Interval)
	case KindReboot:
		if s.RebootDelay > 0 {
			return fmt.Sprintf("@reboot +%s", formatDurationCompact(s.RebootDelay))
		}
		return "@reboot"
	default:
		return s.Raw
	}
}

var standardCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// Next returns the next n scheduled trigger times after from.
// For KindReboot schedules, Next returns nil, nil since reboot triggers are event-driven.
func (s Schedule) Next(from time.Time, n int) ([]time.Time, error) {
	if n <= 0 {
		return nil, nil
	}
	switch s.Kind {
	case KindReboot:
		return nil, nil
	case KindInterval:
		if s.Interval <= 0 {
			return nil, errors.New("interval must be positive")
		}
		out := make([]time.Time, 0, n)
		cur := from
		for range n {
			cur = cur.Add(s.Interval)
			out = append(out, cur)
		}
		return out, nil
	case KindCron:
		cs, err := standardCronParser.Parse(s.CronExpr)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression %q: %w", s.CronExpr, err)
		}
		out := make([]time.Time, 0, n)
		cur := from
		for range n {
			cur = cs.Next(cur)
			out = append(out, cur)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported schedule kind %v", s.Kind)
	}
}

// Parser parses a schedule expression (standard cron, @descriptors, or natural language).
type Parser interface {
	Parse(expr string) (Schedule, error)
}

// HybridParser implements Parser supporting standard 5-field cron syntax,
// cron @descriptors, and human-friendly natural language expressions.
type HybridParser struct{}

// NewHybridParser returns a new HybridParser.
func NewHybridParser() *HybridParser {
	return &HybridParser{}
}

var (
	reAfterReboot   = regexp.MustCompile(`^(.+?)\s+after\s+(?:the\s+)?(?:system\s+)?(?:@?reboot|boot|startup)$`)
	reRebootPlus    = regexp.MustCompile(`^(?:@reboot|reboot|on reboot|at reboot|on boot|at boot|on startup|at startup|startup)\s*\+\s*(.+)$`)
	reDurationPart  = regexp.MustCompile(`^(?:(a|an)\s+([a-z]+)|(\d+)\s*([a-z]+))`)
	reHourlyAt      = regexp.MustCompile(`^(?:every hour|hourly) at (?::|minute\s+)(\d{1,2})$`)
	reMonthlyOn     = regexp.MustCompile(`^(?:monthly|every month) on (?:the |day )?(\d{1,2})(?:st|nd|rd|th)?(?:\s+at\s+(.+))?$`)
	reEveryNumUnit  = regexp.MustCompile(`^every\s+(\d+)\s*([a-z]+)$`)
	reEverySingular = regexp.MustCompile(`^every\s+(second|sec|minute|min|hour|hr|day|week|month|year)$`)
	reTime12        = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)$`)
	reTime24        = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

var rebootAliases = map[string]struct{}{
	"@reboot":       {},
	"reboot":        {},
	"on reboot":     {},
	"at reboot":     {},
	"after reboot":  {},
	"on boot":       {},
	"at boot":       {},
	"after boot":    {},
	"on startup":    {},
	"at startup":    {},
	"after startup": {},
	"startup":       {},
}

var durationUnitMap = map[string]time.Duration{
	"ms":           time.Millisecond,
	"msec":         time.Millisecond,
	"msecs":        time.Millisecond,
	"millisecond":  time.Millisecond,
	"milliseconds": time.Millisecond,
	"s":            time.Second,
	"sec":          time.Second,
	"secs":         time.Second,
	"second":       time.Second,
	"seconds":      time.Second,
	"m":            time.Minute,
	"min":          time.Minute,
	"mins":         time.Minute,
	"minute":       time.Minute,
	"minutes":      time.Minute,
	"h":            time.Hour,
	"hr":           time.Hour,
	"hrs":          time.Hour,
	"hour":         time.Hour,
	"hours":        time.Hour,
	"d":            24 * time.Hour,
	"day":          24 * time.Hour,
	"days":         24 * time.Hour,
	"w":            7 * 24 * time.Hour,
	"wk":           7 * 24 * time.Hour,
	"wks":          7 * 24 * time.Hour,
	"week":         7 * 24 * time.Hour,
	"weeks":        7 * 24 * time.Hour,
}

var descriptorCron = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var singleWordCron = map[string]string{
	"hourly":   "0 * * * *",
	"daily":    "0 0 * * *",
	"weekly":   "0 0 * * 0",
	"monthly":  "0 0 1 * *",
	"yearly":   "0 0 1 1 *",
	"annually": "0 0 1 1 *",
}

var dayOfWeekMap = map[string]int{
	"sun": 0, "sunday": 0, "sundays": 0,
	"mon": 1, "monday": 1, "mondays": 1,
	"tue": 2, "tues": 2, "tuesday": 2, "tuesdays": 2,
	"wed": 3, "weds": 3, "wednesday": 3, "wednesdays": 3,
	"thu": 4, "thur": 4, "thurs": 4, "thursday": 4, "thursdays": 4,
	"fri": 5, "friday": 5, "fridays": 5,
	"sat": 6, "saturday": 6, "saturdays": 6,
}

// Parse parses a hybrid schedule string into a normalized Schedule.
func (p *HybridParser) Parse(expr string) (Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return Schedule{}, errors.New("schedule expression cannot be empty")
	}

	norm := normalizeSpaces(strings.ToLower(raw))

	// 1. Reboot triggers (immediate or delayed, e.g. "on reboot", "5 minutes after reboot", "@reboot + 5m").
	if s, ok, err := parseRebootSchedule(raw, norm); ok || err != nil {
		return s, err
	}

	// 2. @every <duration> and @descriptors.
	if strings.HasPrefix(norm, "@every ") {
		durStr := strings.TrimSpace(strings.TrimPrefix(norm, "@every "))
		d, err := time.ParseDuration(durStr)
		if err != nil || d <= 0 {
			return Schedule{}, fmt.Errorf("invalid @every duration %q", raw)
		}
		return Schedule{Raw: raw, Kind: KindInterval, Interval: d}, nil
	}
	if cronExpr, ok := descriptorCron[norm]; ok {
		return Schedule{Raw: raw, Kind: KindCron, CronExpr: cronExpr}, nil
	}

	// 3. Standard 5-field cron expression.
	if fields := strings.Fields(raw); len(fields) == 5 {
		if _, err := standardCronParser.Parse(raw); err == nil {
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: strings.Join(fields, " ")}, nil
		}
		// Only return a cron error immediately if it looks like a cron expression
		// (doesn't start with English keywords).
		first := strings.ToLower(fields[0])
		if !isNaturalLead(first) {
			_, err := standardCronParser.Parse(raw)
			return Schedule{}, fmt.Errorf("invalid cron expression %q: %w", raw, err)
		}
	}

	// 4. Single-word natural shorthands ("hourly", "daily", ...).
	if cronExpr, ok := singleWordCron[norm]; ok {
		return Schedule{Raw: raw, Kind: KindCron, CronExpr: cronExpr}, nil
	}

	// 5. Hourly at minute ("every hour at :15", "hourly at :30").
	if m := reHourlyAt.FindStringSubmatch(norm); m != nil {
		min, err := strconv.Atoi(m[1])
		if err != nil || min < 0 || min > 59 {
			return Schedule{}, fmt.Errorf("invalid minute in %q: must be 0-59", raw)
		}
		return Schedule{
			Raw:      raw,
			Kind:     KindCron,
			CronExpr: fmt.Sprintf("%d * * * *", min),
		}, nil
	}

	// 6. Monthly on day ("monthly on the 1st at 9am", "every month on day 15").
	if m := reMonthlyOn.FindStringSubmatch(norm); m != nil {
		dom, err := strconv.Atoi(m[1])
		if err != nil || dom < 1 || dom > 31 {
			return Schedule{}, fmt.Errorf("invalid day of month in %q: must be 1-31", raw)
		}
		hour, min := 0, 0
		if m[2] != "" {
			hour, min, err = parseTimeOfDay(m[2])
			if err != nil {
				return Schedule{}, fmt.Errorf("invalid time in %q: %w", raw, err)
			}
		}
		return Schedule{
			Raw:      raw,
			Kind:     KindCron,
			CronExpr: fmt.Sprintf("%d %d %d * *", min, hour, dom),
		}, nil
	}

	// 7. Time-only daily shorthand ("at 9am", "at 14:30").
	if strings.HasPrefix(norm, "at ") {
		hour, min, err := parseTimeOfDay(strings.TrimPrefix(norm, "at "))
		if err != nil {
			return Schedule{}, fmt.Errorf("invalid schedule %q: %w", raw, err)
		}
		return Schedule{
			Raw:      raw,
			Kind:     KindCron,
			CronExpr: fmt.Sprintf("%d %d * * *", min, hour),
		}, nil
	}

	// 8. Daily / weekday / weekend / specific days of week (with optional "at <time>").
	if s, ok, err := parseDaySchedule(raw, norm); ok || err != nil {
		return s, err
	}

	// 9. Interval expressions ("every 5 minutes", "every 30s", "every 1h30m", "every hour").
	if s, ok, err := parseEveryInterval(raw, norm); ok || err != nil {
		return s, err
	}

	return Schedule{}, fmt.Errorf("unrecognized schedule %q (expected 5-field cron, @descriptor, or natural language like 'every hour', 'daily at 9am', 'on reboot')", raw)
}

func isNaturalLead(word string) bool {
	switch word {
	case "every", "daily", "weekly", "monthly", "hourly", "at", "on", "weekdays", "weekends":
		return true
	}
	_, isDay := dayOfWeekMap[word]
	return isDay
}

func parseDaySchedule(raw, norm string) (Schedule, bool, error) {
	subject := norm
	timePart := ""
	if idx := strings.LastIndex(norm, " at "); idx != -1 {
		subject = strings.TrimSpace(norm[:idx])
		timePart = strings.TrimSpace(norm[idx+4:])
	}

	var dowExpr string
	switch subject {
	case "daily", "every day":
		if timePart == "" {
			// Let "every day" fall through or handle directly.
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 * * *"}, true, nil
		}
		dowExpr = "*"
	case "every weekday", "weekdays", "every weekdays":
		dowExpr = "1-5"
	case "every weekend", "weekends", "every weekends":
		dowExpr = "0,6"
	default:
		// Check "weekly on <days>", "every week on <days>", "every <days>", or "<days>".
		daysCandidate := subject
		switch {
		case strings.HasPrefix(daysCandidate, "weekly on "):
			daysCandidate = strings.TrimPrefix(daysCandidate, "weekly on ")
		case strings.HasPrefix(daysCandidate, "every week on "):
			daysCandidate = strings.TrimPrefix(daysCandidate, "every week on ")
		case strings.HasPrefix(daysCandidate, "every "):
			daysCandidate = strings.TrimPrefix(daysCandidate, "every ")
		}

		days, ok := parseDayList(daysCandidate)
		if !ok {
			return Schedule{}, false, nil
		}
		dowExpr = formatDayList(days)
	}

	hour, min := 0, 0
	if timePart != "" {
		var err error
		hour, min, err = parseTimeOfDay(timePart)
		if err != nil {
			return Schedule{}, true, fmt.Errorf("invalid time in %q: %w", raw, err)
		}
	}

	return Schedule{
		Raw:      raw,
		Kind:     KindCron,
		CronExpr: fmt.Sprintf("%d %d * * %s", min, hour, dowExpr),
	}, true, nil
}

func parseEveryInterval(raw, norm string) (Schedule, bool, error) {
	if !strings.HasPrefix(norm, "every ") {
		return Schedule{}, false, nil
	}

	// Singular: "every second", "every minute", "every hour", "every day", "every week", "every month", "every year".
	if m := reEverySingular.FindStringSubmatch(norm); m != nil {
		switch m[1] {
		case "second", "sec":
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Second}, true, nil
		case "minute", "min":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "* * * * *"}, true, nil
		case "hour", "hr":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 * * * *"}, true, nil
		case "day":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 * * *"}, true, nil
		case "week":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 * * 0"}, true, nil
		case "month":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 1 * *"}, true, nil
		case "year":
			return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 1 1 *"}, true, nil
		}
	}

	// Numeric + unit: "every 5 minutes", "every 2 hours", "every 30s", "every 3 days".
	if m := reEveryNumUnit.FindStringSubmatch(norm); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			return Schedule{}, true, fmt.Errorf("invalid interval count in %q: must be > 0", raw)
		}
		unit := m[2]
		switch unit {
		case "s", "sec", "secs", "second", "seconds":
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Duration(n) * time.Second}, true, nil
		case "m", "min", "mins", "minute", "minutes":
			if n == 1 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: "* * * * *"}, true, nil
			}
			if n < 60 && 60%n == 0 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: fmt.Sprintf("*/%d * * * *", n)}, true, nil
			}
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Duration(n) * time.Minute}, true, nil
		case "h", "hr", "hrs", "hour", "hours":
			if n == 1 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 * * * *"}, true, nil
			}
			if n < 24 && 24%n == 0 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: fmt.Sprintf("0 */%d * * *", n)}, true, nil
			}
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Duration(n) * time.Hour}, true, nil
		case "d", "day", "days":
			if n == 1 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 * * *"}, true, nil
			}
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Duration(n) * 24 * time.Hour}, true, nil
		case "w", "wk", "wks", "week", "weeks":
			if n == 1 {
				return Schedule{Raw: raw, Kind: KindCron, CronExpr: "0 0 * * 0"}, true, nil
			}
			return Schedule{Raw: raw, Kind: KindInterval, Interval: time.Duration(n) * 7 * 24 * time.Hour}, true, nil
		}
	}

	// Compound Go durations like "every 1h30m" or "every 2m30s".
	rest := strings.TrimSpace(strings.TrimPrefix(norm, "every "))
	if d, err := time.ParseDuration(rest); err == nil {
		if d <= 0 {
			return Schedule{}, true, fmt.Errorf("invalid interval duration in %q: must be > 0", raw)
		}
		return Schedule{Raw: raw, Kind: KindInterval, Interval: d}, true, nil
	}

	return Schedule{}, false, nil
}

func parseDayList(s string) ([]int, bool) {
	// Support "mon, wed, fri", "monday and thursday", "mon, wed and fri".
	replaced := strings.ReplaceAll(s, " and ", ",")
	parts := strings.Split(replaced, ",")
	if len(parts) == 0 {
		return nil, false
	}

	seen := make(map[int]struct{}, len(parts))
	days := make([]int, 0, len(parts))
	for _, p := range parts {
		tok := strings.TrimSpace(p)
		if tok == "" {
			return nil, false
		}
		d, ok := dayOfWeekMap[tok]
		if !ok {
			return nil, false
		}
		if _, dup := seen[d]; !dup {
			seen[d] = struct{}{}
			days = append(days, d)
		}
	}
	sort.Ints(days)
	return days, len(days) > 0
}

func formatDayList(days []int) string {
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = strconv.Itoa(d)
	}
	return strings.Join(parts, ",")
}

func parseTimeOfDay(s string) (hour, min int, err error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "midnight":
		return 0, 0, nil
	case "noon":
		return 12, 0, nil
	}

	if m := reTime12.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mn := 0
		if m[2] != "" {
			mn, _ = strconv.Atoi(m[2])
		}
		if h < 1 || h > 12 || mn < 0 || mn > 59 {
			return 0, 0, fmt.Errorf("invalid 12-hour time %q", s)
		}
		if m[3] == "am" {
			if h == 12 {
				h = 0
			}
		} else {
			if h != 12 {
				h += 12
			}
		}
		return h, mn, nil
	}

	if m := reTime24.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mn, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || mn < 0 || mn > 59 {
			return 0, 0, fmt.Errorf("invalid 24-hour time %q", s)
		}
		return h, mn, nil
	}

	return 0, 0, fmt.Errorf("unrecognized time %q (examples: 9am, 8:30pm, 14:00, midnight, noon)", s)
}

func parseRebootSchedule(raw, norm string) (Schedule, bool, error) {
	if _, ok := rebootAliases[norm]; ok {
		return Schedule{Raw: raw, Kind: KindReboot}, true, nil
	}

	var delayStr string
	if m := reAfterReboot.FindStringSubmatch(norm); m != nil {
		delayStr = strings.TrimSpace(m[1])
	} else if m := reRebootPlus.FindStringSubmatch(norm); m != nil {
		delayStr = strings.TrimSpace(m[1])
	} else {
		return Schedule{}, false, nil
	}

	d, err := parseDurationExpr(delayStr)
	if err != nil {
		return Schedule{}, true, fmt.Errorf("invalid reboot delay in %q: %w", raw, err)
	}
	return Schedule{
		Raw:         raw,
		Kind:        KindReboot,
		RebootDelay: d,
	}, true, nil
}

func parseDurationExpr(s string) (time.Duration, error) {
	s = normalizeSpaces(strings.ToLower(strings.TrimSpace(s)))
	if s == "" {
		return 0, errors.New("duration cannot be empty")
	}

	// Standard Go duration syntax (e.g. "5m", "30s", "1h30m", "500ms").
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, errors.New("duration must be > 0")
		}
		return d, nil
	}

	// Natural-language single or compound durations (e.g. "5 minutes", "a minute",
	// "1 hour 30 minutes", "1 hour and 30 minutes", "2m 30s", "1 day").
	var total time.Duration
	rest := s
	for {
		m := reDurationPart.FindStringSubmatch(rest)
		if m == nil {
			return 0, fmt.Errorf("unrecognized duration %q", s)
		}

		var (
			n       int
			unitStr string
		)
		if m[1] != "" {
			n = 1
			unitStr = m[2]
		} else {
			var err error
			n, err = strconv.Atoi(m[3])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("duration count must be > 0 in %q", s)
			}
			unitStr = m[4]
		}

		unitDur, ok := durationUnitMap[unitStr]
		if !ok {
			return 0, fmt.Errorf("unrecognized duration unit %q in %q", unitStr, s)
		}

		if int64(n) > math.MaxInt64/int64(unitDur) {
			return 0, fmt.Errorf("duration overflow in %q", s)
		}
		part := time.Duration(n) * unitDur
		if int64(total) > math.MaxInt64-int64(part) {
			return 0, fmt.Errorf("duration overflow in %q", s)
		}
		total += part

		rest = strings.TrimSpace(rest[len(m[0]):])
		if rest == "" {
			break
		}

		// Optional separator between compound parts: ",", "and", ", and".
		hadSep := false
		if strings.HasPrefix(rest, ",") {
			hadSep = true
			rest = strings.TrimSpace(rest[1:])
		}
		if strings.HasPrefix(rest, "and ") {
			hadSep = true
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "and "))
		} else if rest == "and" {
			return 0, fmt.Errorf("trailing 'and' in duration %q", s)
		}
		if hadSep && rest == "" {
			return 0, fmt.Errorf("trailing separator in duration %q", s)
		}
	}

	if total <= 0 {
		return 0, errors.New("duration must be > 0")
	}
	return total, nil
}

func formatDurationCompact(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
