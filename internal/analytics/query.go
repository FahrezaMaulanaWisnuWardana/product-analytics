package analytics

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Point struct {
	Date      string `json:"date"`
	Users     int    `json:"users"`
	Events    int    `json:"events"`
	Sessions  int    `json:"sessions"`
	Pageviews int    `json:"pageviews"`
}
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Users int    `json:"users"`
}
type Metrics struct {
	Users     int `json:"users"`
	Events    int `json:"events"`
	Sessions  int `json:"sessions"`
	Pageviews int `json:"pageviews"`
}

// DecisionContext describes whether the selected sample is broad enough for
// product decisions and where activity is concentrated. Readiness is a
// deliberately conservative heuristic, not a statistical significance test.
type DecisionContext struct {
	Readiness       string  `json:"readiness"`
	ActiveDays      int     `json:"active_days"`
	FirstEvent      string  `json:"first_event,omitempty"`
	LastEvent       string  `json:"last_event,omitempty"`
	TopEvent        Count   `json:"top_event"`
	TopEventShare   float64 `json:"top_event_share"`
	TopPersonEvents int     `json:"top_person_events"`
	TopPersonShare  float64 `json:"top_person_share"`
}

func Measure(events []Event) Metrics {
	users := map[string]bool{}
	sessions := map[string]bool{}
	m := Metrics{Events: len(events)}
	for _, e := range events {
		users[e.DistinctID] = true
		if e.SessionID != "" {
			sessions[e.DistinctID+"\x00"+e.SessionID] = true
		}
		if e.Name == "$pageview" {
			m.Pageviews++
		}
	}
	m.Users = len(users)
	m.Sessions = len(sessions)
	return m
}

func Decision(events []Event) DecisionContext {
	result := DecisionContext{Readiness: "collecting"}
	if len(events) == 0 {
		return result
	}

	days := map[string]bool{}
	people := map[string]int{}
	first, last := events[0].Timestamp, events[0].Timestamp
	for _, e := range events {
		days[e.Timestamp.UTC().Format("2006-01-02")] = true
		people[e.DistinctID]++
		if e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if e.Timestamp.After(last) {
			last = e.Timestamp
		}
	}

	result.ActiveDays = len(days)
	result.FirstEvent = first.UTC().Format(time.RFC3339)
	result.LastEvent = last.UTC().Format(time.RFC3339)
	if breakdown := Breakdown(events, ""); len(breakdown) > 0 {
		result.TopEvent = breakdown[0]
		result.TopEventShare = float64(breakdown[0].Count) * 100 / float64(len(events))
	}
	for _, count := range people {
		if count > result.TopPersonEvents {
			result.TopPersonEvents = count
		}
	}
	result.TopPersonShare = float64(result.TopPersonEvents) * 100 / float64(len(events))

	users := len(people)
	switch {
	case users >= 30 && result.ActiveDays >= 7:
		result.Readiness = "ready"
	case users >= 10 && result.ActiveDays >= 3:
		result.Readiness = "directional"
	}
	return result
}
func Series(events []Event, from, to time.Time) []Point {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	sessions := map[string]map[string]bool{}
	pageviews := map[string]int{}
	for _, e := range events {
		k := e.Timestamp.UTC().Format("2006-01-02")
		counts[k]++
		if users[k] == nil {
			users[k] = map[string]bool{}
		}
		users[k][e.DistinctID] = true
		if sessions[k] == nil {
			sessions[k] = map[string]bool{}
		}
		if e.SessionID != "" {
			sessions[k][e.DistinctID+"\x00"+e.SessionID] = true
		}
		if e.Name == "$pageview" {
			pageviews[k]++
		}
	}
	out := []Point{}
	for d := from.UTC().Truncate(24 * time.Hour); d.Before(to); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		out = append(out, Point{k, len(users[k]), counts[k], len(sessions[k]), pageviews[k]})
	}
	return out
}
func Breakdown(events []Event, property string) []Count {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	for _, e := range events {
		key := e.Name
		if property != "" {
			key, _ = e.Properties[property].(string)
			if key == "" {
				key = "Unknown"
			}
		}
		counts[key]++
		if users[key] == nil {
			users[key] = map[string]bool{}
		}
		users[key][e.DistinctID] = true
	}
	out := []Count{}
	for k, n := range counts {
		out = append(out, Count{k, n, len(users[k])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	return out
}

type FunnelStep struct {
	Name          string   `json:"name"`
	Users         int      `json:"users"`
	Rate          float64  `json:"rate"`
	Dropoff       int      `json:"dropoff"`
	People        []string `json:"people"`
	DropoffPeople []string `json:"dropoff_people"`
}

// Funnel counts people completing steps in order within a conversion window.
func Funnel(events []Event, steps []string, window time.Duration) []FunnelStep {
	sorted := append([]Event{}, events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	type progress struct {
		next  int
		start time.Time
	}
	states := map[string]progress{}
	reached := make([]map[string]bool, len(steps))
	for i := range reached {
		reached[i] = map[string]bool{}
	}
	for _, e := range sorted {
		p := states[e.DistinctID]
		if p.next > 0 && e.Timestamp.Sub(p.start) > window {
			p = progress{}
		}
		if p.next < len(steps) && e.Name == steps[p.next] {
			if p.next == 0 {
				p.start = e.Timestamp
			}
			reached[p.next][e.DistinctID] = true
			p.next++
		}
		states[e.DistinctID] = p
	}
	out := []FunnelStep{}
	base := 0
	if len(steps) > 0 {
		base = len(reached[0])
	}
	for i, n := range steps {
		v := len(reached[i])
		rate := 0.0
		if base > 0 {
			rate = float64(v) / float64(base) * 100
		}
		drop := 0
		if i > 0 {
			drop = len(reached[i-1]) - v
		}
		people := make([]string, 0, len(reached[i]))
		for id := range reached[i] {
			people = append(people, id)
		}
		sort.Strings(people)
		dropped := []string{}
		if i > 0 {
			for id := range reached[i-1] {
				if !reached[i][id] {
					dropped = append(dropped, id)
				}
			}
			sort.Strings(dropped)
		}
		out = append(out, FunnelStep{Name: n, Users: v, Rate: rate, Dropoff: drop, People: people, DropoffPeople: dropped})
	}
	return out
}

// CanonicalizeIdentity joins anonymous activity to the identified person when
// the browser SDK supplies the previous anonymous ID on an $identify event.
func CanonicalizeIdentity(events []Event) []Event {
	aliases := map[string]string{}
	for _, e := range events {
		if e.Name != "$identify" {
			continue
		}
		if old, ok := e.Properties["$anon_distinct_id"].(string); ok && old != "" && old != e.DistinctID {
			aliases[old] = e.DistinctID
		}
	}
	canonical := func(id string) string {
		seen := map[string]bool{}
		for aliases[id] != "" && !seen[id] {
			seen[id] = true
			id = aliases[id]
		}
		return id
	}
	out := append([]Event{}, events...)
	for i := range out {
		out[i].DistinctID = canonical(out[i].DistinctID)
	}
	return out
}

// FilterProperties applies the same exact property segment to every report.
func FilterProperties(events []Event, property, value string) []Event {
	property, value = strings.TrimSpace(property), strings.TrimSpace(value)
	if property == "" || value == "" {
		return events
	}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if fmt.Sprint(e.Properties[property]) == value {
			out = append(out, e)
		}
	}
	return out
}

type EventDefinition struct {
	Name       string            `json:"name"`
	Count      int               `json:"count"`
	Users      int               `json:"users"`
	FirstSeen  time.Time         `json:"first_seen"`
	LastSeen   time.Time         `json:"last_seen"`
	Properties map[string]string `json:"properties"`
	Warnings   []string          `json:"warnings"`
}

type TrackingHealth struct {
	Events         int               `json:"events"`
	EventTypes     int               `json:"event_types"`
	IdentifiedRate float64           `json:"identified_rate"`
	SessionRate    float64           `json:"session_rate"`
	Definitions    []EventDefinition `json:"definitions"`
}

func valueType(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case float64, float32, int, int64, int32:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// Catalog makes instrumentation quality visible before it affects decisions.
func Catalog(events []Event) TrackingHealth {
	type accumulator struct {
		definition EventDefinition
		people     map[string]bool
		types      map[string]map[string]bool
	}
	byName := map[string]*accumulator{}
	identified, sessions := 0, 0
	for _, e := range events {
		if !strings.HasPrefix(e.DistinctID, "p_") {
			identified++
		}
		if e.SessionID != "" {
			sessions++
		}
		a := byName[e.Name]
		if a == nil {
			a = &accumulator{definition: EventDefinition{Name: e.Name, FirstSeen: e.Timestamp, LastSeen: e.Timestamp, Properties: map[string]string{}, Warnings: []string{}}, people: map[string]bool{}, types: map[string]map[string]bool{}}
			byName[e.Name] = a
		}
		a.definition.Count++
		a.people[e.DistinctID] = true
		if e.Timestamp.Before(a.definition.FirstSeen) {
			a.definition.FirstSeen = e.Timestamp
		}
		if e.Timestamp.After(a.definition.LastSeen) {
			a.definition.LastSeen = e.Timestamp
		}
		for key, value := range e.Properties {
			if a.types[key] == nil {
				a.types[key] = map[string]bool{}
			}
			a.types[key][valueType(value)] = true
		}
	}
	definitions := make([]EventDefinition, 0, len(byName))
	for _, a := range byName {
		a.definition.Users = len(a.people)
		for property, types := range a.types {
			names := make([]string, 0, len(types))
			for name := range types {
				names = append(names, name)
			}
			sort.Strings(names)
			a.definition.Properties[property] = strings.Join(names, " | ")
			if len(names) > 1 {
				a.definition.Warnings = append(a.definition.Warnings, property+" changes type")
			}
		}
		sort.Strings(a.definition.Warnings)
		definitions = append(definitions, a.definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Count > definitions[j].Count })
	health := TrackingHealth{Events: len(events), EventTypes: len(definitions), Definitions: definitions}
	if len(events) > 0 {
		health.IdentifiedRate = float64(identified) * 100 / float64(len(events))
		health.SessionRate = float64(sessions) * 100 / float64(len(events))
	}
	return health
}

type PathEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
	Users int    `json:"users"`
}

// Paths returns the most common consecutive event transitions per person.
func Paths(events []Event) []PathEdge {
	sorted := append([]Event{}, events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	last := map[string]string{}
	counts := map[string]int{}
	people := map[string]map[string]bool{}
	for _, e := range sorted {
		if previous := last[e.DistinctID]; previous != "" && previous != e.Name {
			key := previous + "\x00" + e.Name
			counts[key]++
			if people[key] == nil {
				people[key] = map[string]bool{}
			}
			people[key][e.DistinctID] = true
		}
		last[e.DistinctID] = e.Name
	}
	out := make([]PathEdge, 0, len(counts))
	for key, count := range counts {
		parts := strings.SplitN(key, "\x00", 2)
		out = append(out, PathEdge{From: parts[0], To: parts[1], Count: count, Users: len(people[key])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

type Person struct {
	ID         string         `json:"id"`
	Events     int            `json:"events"`
	LastSeen   time.Time      `json:"last_seen"`
	Properties map[string]any `json:"properties"`
}

func People(events []Event) []Person {
	people := map[string]Person{}
	for _, e := range events {
		p := people[e.DistinctID]
		p.ID = e.DistinctID
		p.Events++
		if e.Timestamp.After(p.LastSeen) {
			p.LastSeen = e.Timestamp
			p.Properties = e.Properties
		}
		people[e.DistinctID] = p
	}
	out := []Person{}
	for _, p := range people {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

type Cohort struct {
	Date     string     `json:"date"`
	Size     int        `json:"size"`
	Retained []*float64 `json:"retained"`
}

// Daily retention uses the person's first observed event in the project's complete history.
func Retention(all []Event, from, to time.Time) []Cohort {
	first := map[string]time.Time{}
	active := map[string]map[string]bool{}
	for _, e := range all {
		d := e.Timestamp.UTC().Truncate(24 * time.Hour)
		if prev, ok := first[e.DistinctID]; !ok || d.Before(prev) {
			first[e.DistinctID] = d
		}
		if active[e.DistinctID] == nil {
			active[e.DistinctID] = map[string]bool{}
		}
		active[e.DistinctID][d.Format("2006-01-02")] = true
	}
	out := []Cohort{}
	for day := from.UTC().Truncate(24 * time.Hour); day.Before(to); day = day.AddDate(0, 0, 1) {
		members := []string{}
		for id, d := range first {
			if d.Equal(day) {
				members = append(members, id)
			}
		}
		c := Cohort{Date: day.Format("2006-01-02"), Size: len(members), Retained: []*float64{}}
		for n := 0; n < 8; n++ {
			d := day.AddDate(0, 0, n)
			var pct *float64
			if d.Before(to) && len(members) > 0 {
				count := 0
				for _, id := range members {
					if active[id][d.Format("2006-01-02")] {
						count++
					}
				}
				v := float64(count) * 100 / float64(len(members))
				pct = &v
			}
			c.Retained = append(c.Retained, pct)
		}
		out = append(out, c)
	}
	return out
}
