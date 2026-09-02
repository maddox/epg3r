package store

// RunStatus is the outcome of a refresh.
type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunOK      RunStatus = "ok"      // every source fetched and parsed
	RunPartial RunStatus = "partial" // some source failed; output built from the rest
	RunFailed  RunStatus = "failed"  // no source produced channels
)

// Trigger says why a refresh ran.
type Trigger string

const (
	TriggerStartup  Trigger = "startup"
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
)

// Outcome is what happened to one playlist entry in a run. The UI groups run
// results by it, so every new outcome needs a home here and nowhere else.
type Outcome string

const (
	OutcomeExported  Outcome = "exported"        // in the guide with at least one programme
	OutcomeIdle      Outcome = "idle"            // in the lineup with nothing scheduled
	OutcomeUnparsed  Outcome = "unparsed"        // slot channel whose title yielded nothing usable
	OutcomeLowConf   Outcome = "low_confidence"  // parsed, but below the confidence threshold
	OutcomeUnmatched Outcome = "unmatched_group" // no league recognised
	OutcomeDuplicate Outcome = "duplicate"       // another entry already holds this channel id
	OutcomeNetwork   Outcome = "network"         // a broadcast network, not an event channel
)

// FetchStatus is how a source's last download went.
type FetchStatus string

const (
	FetchFresh       FetchStatus = "ok"           // downloaded
	FetchNotModified FetchStatus = "not-modified" // server confirmed the cached copy
	FetchStale       FetchStatus = "cached"       // request failed; cached copy used
	FetchFailed      FetchStatus = "error"        // nothing usable
)
