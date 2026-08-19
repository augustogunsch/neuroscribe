package main

// Account plans. Quotas exist to stop one account filling the disk, not to
// nag: nothing in the day-to-day interface mentions them, and a person only
// meets a limit when they actually reach it or go looking in settings.

import "fmt"

type plan struct {
	Key       string
	Name      string
	MaxNotes  int
	MaxImages int
	// per image, and per chapter of ciphertext (roughly a third larger than
	// the text it encrypts)
	MaxImageBytes   int64
	MaxChapterBytes int
	// every sealed record, of any kind: the backstop that keeps chapters,
	// folders and types from being an unmetered way to fill the disk
	MaxRecords int
}

var plans = map[string]plan{
	"free": {
		Key:             "free",
		Name:            "Free",
		MaxNotes:        500,
		MaxImages:       20,
		MaxImageBytes:   5 << 20,
		MaxChapterBytes: 500 << 10,
		MaxRecords:      25000,
	},
	"premium": {
		Key:             "premium",
		Name:            "Premium",
		MaxNotes:        5000,
		MaxImages:       500,
		MaxImageBytes:   10 << 20,
		MaxChapterBytes: 8 << 20,
		MaxRecords:      250000,
	},
}

const defaultPlan = "free"

func planFor(key string) plan {
	if p, ok := plans[key]; ok {
		return p
	}
	return plans[defaultPlan]
}

func validPlan(key string) bool {
	_, ok := plans[key]
	return ok
}

// plan returns the quotas this account is held to.
func (st *store) plan() plan {
	var key string
	st.db.QueryRow("SELECT plan FROM users WHERE id = ?", st.uid).Scan(&key)
	return planFor(key)
}

// usage is what the settings page reports; it is the only place quotas are
// visible until one is hit.
type usage struct {
	Plan     plan
	Notes    int
	Images   int
	ImageCap string
	NoteCap  string
}

// humanBytes prints a limit the way the settings page should read it: whole
// units, and the largest one that does not go fractional.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func (p plan) String() string {
	return fmt.Sprintf("%s (%d notes, %d images)", p.Name, p.MaxNotes, p.MaxImages)
}
