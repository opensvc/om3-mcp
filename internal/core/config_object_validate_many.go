package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opensvc/om3-mcp/internal/client"
)

// The bounds of a validation of many objects: the objects validated by a
// call, a cursor continuing, well under the burst of 100 requests the daemon
// listener grants an address; the validations sent to the daemons at once,
// as the webapp does; the requests that did not reach a node before the next
// objects of that node are not sent; the alerts listed and their comments
// together; the objects refused named; the failures grouped, and the paths
// of each.
const (
	maxValidatedObjects               = 50
	configValidationParallel          = 4
	maxConfigValidationNodeFailures   = 3
	maxConfigValidationsAlerts        = 100
	maxConfigValidationsCommentsBytes = 32 << 10
	maxConfigValidationsNamed         = 20
	maxConfigValidationFailureGroups  = 10
	maxConfigValidationFailurePaths   = 10
)

// configValidationsDeadline is the time of a call, after which it answers
// what was validated; a variable for the tests.
var configValidationsDeadline = 30 * time.Second

// The reasons a call validated fewer objects than it could.
const (
	stoppedThrottled = "throttled"
	stoppedDeadline  = "deadline"
)

// configValidationsEmptyNote is the note of a selector naming no object.
const configValidationsEmptyNote = "no object of this selector is visible to the caller: check its spelling, such as */svc/* for the services, or list_cluster_objects for the objects there are"

type ValidateObjectConfigsOptions struct {
	Selector string
	Cursor   string
}

// ObjectConfigValidations is the validation of the configurations of the
// objects of a selector, each on its first node: which are in error, in
// warning, valid, or could not be validated.
type ObjectConfigValidations struct {
	Provenance     Provenance                      `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Selector       string                          `json:"selector" jsonschema:"the OpenSVC object selector validated"`
	Total          int                             `json:"total" jsonschema:"the number of objects the selector names for the caller, the cluster configuration left out"`
	Validated      int                             `json:"validated" jsonschema:"the number of objects this call validated, at most 50"`
	ErrorObjects   int                             `json:"error_objects" jsonschema:"the objects of this call with one error at least: add those of the calls continuing with a cursor"`
	WarningObjects int                             `json:"warning_objects" jsonschema:"the objects of this call with warnings only: add those of the calls continuing with a cursor"`
	ValidObjects   int                             `json:"valid_objects" jsonschema:"the objects of this call without alert"`
	RefusedObjects int                             `json:"refused_objects" jsonschema:"the objects of this call the daemon refused to validate for the caller: an administrator of their namespace or root may"`
	FailedObjects  int                             `json:"failed_objects" jsonschema:"the objects of this call whose validation failed otherwise, such as a node not answering: not valid, not known"`
	Objects        []ObjectConfigValidationSummary `json:"objects" jsonschema:"every object of this call in error or in warning, the errors first, then by path"`
	Refused        []string                        `json:"refused,omitempty" jsonschema:"the paths refused, at most 20"`
	Failed         []ObjectConfigValidationFailure `json:"failed,omitempty" jsonschema:"the failed validations grouped by reason, the most frequent first, at most 10 reasons"`
	Truncated      bool                            `json:"truncated" jsonschema:"whether objects of the selector remain to validate, which may be in error: call again with cursor set to next_cursor until it is false before concluding"`
	NextCursor     string                          `json:"next_cursor,omitempty" jsonschema:"the cursor to pass to validate the next objects"`
	Stopped        string                          `json:"stopped,omitempty" jsonschema:"why this call validated fewer objects than it could: throttled, the daemon refusing more requests for now, call again in a few seconds; deadline, the 30 seconds of a call spent"`
	Note           string                          `json:"note,omitempty" jsonschema:"set when the selector names no object visible to the caller"`
	TermNote       string                          `json:"term_note,omitempty" jsonschema:"set when the selector holds a configuration term: what the daemon could not see"`
}

// ObjectConfigValidationSummary is an object in error or warning.
type ObjectConfigValidationSummary struct {
	Path            string                  `json:"path" jsonschema:"the canonical OpenSVC object path"`
	Node            string                  `json:"node" jsonschema:"the node it was validated on: the first by name of those holding its configuration, else of its scope"`
	Level           string                  `json:"level" jsonschema:"error or warning, on that node"`
	ErrorCount      int                     `json:"error_count" jsonschema:"the number of its alerts of level error"`
	WarningCount    int                     `json:"warning_count" jsonschema:"the number of its alerts of level warning"`
	Alerts          []ConfigValidationAlert `json:"alerts" jsonschema:"its first alerts, the errors first, then by key: 100 alerts are shared among the objects of a call"`
	AlertsTruncated bool                    `json:"alerts_truncated" jsonschema:"whether it has more alerts than listed: validate_object_config gives them all"`
}

// ObjectConfigValidationFailure is a reason validations failed for.
type ObjectConfigValidationFailure struct {
	Reason  string   `json:"reason" jsonschema:"why, at most 255 bytes"`
	Objects int      `json:"objects" jsonschema:"the number of objects that failed for it"`
	Paths   []string `json:"paths" jsonschema:"their first paths, at most 10"`
}

// configValidationOutcome is the validation of an object of a page: ran
// false when it was not sent, or cut by the end of the call, and is to be
// validated again by the next call.
type configValidationOutcome struct {
	ran        bool
	validation ObjectConfigValidation
	err        error
}

// ValidateObjectConfigs validates the configurations of the objects a
// selector names, ** by default, each on its first node, as the webapp does
// for its list: the answer to which objects have a configuration in error or
// in warning, in one call where validate_object_config would take one an
// object. The objects come from one read of their status, which tells the
// nodes holding each configuration; the validations go a few at a time, at
// most maxValidatedObjects a call, a cursor continuing. The cluster
// configuration is left out: get_cluster_status tells its issues, and the
// daemon validates it for root and join only.
//
// A daemon throttling the requests, or the deadline of the call, ends the
// call at the last object validated in order, a cursor continuing there: an
// object not validated is never counted valid. A daemon refusing the token,
// or answering in a way this MCP cannot use, fails the call, as a page whose
// validations all failed does.
func (s *Service) ValidateObjectConfigs(ctx context.Context, options ValidateObjectConfigsOptions) (ObjectConfigValidations, error) {
	selector := strings.TrimSpace(options.Selector)
	if selector == "" {
		selector = "**"
	}
	if len(selector) > maxObjectSelectorLength {
		return ObjectConfigValidations{}, fmt.Errorf("object selector exceeds %d characters", maxObjectSelectorLength)
	}
	if len(options.Cursor) > maxObjectCursorLength {
		return ObjectConfigValidations{}, fmt.Errorf("object cursor exceeds %d characters", maxObjectCursorLength)
	}
	configuration, err := checkSelectorTerms(selector)
	if err != nil {
		return ObjectConfigValidations{}, err
	}

	var response daemonObjectList
	if err := s.client.GetJSON(ctx, "/api/object", url.Values{"path": {selector}}, &response); err != nil {
		return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: read the objects: %w", selector, err)
	}
	if response.Kind != "ObjectList" || response.Items == nil {
		return ObjectConfigValidations{}, shapeErrorf("validate the configurations of %s: the daemon answered %q, not an ObjectList with its items", selector, response.Kind)
	}
	targets := make([]configValidationTarget, 0, len(response.Items))
	seen := make(map[string]bool, len(response.Items))
	for _, item := range response.Items {
		reference, err := parseClusterObjectReference(item.Meta.Object)
		if err != nil {
			return ObjectConfigValidations{}, shapeErrorf("validate the configurations of %s: the daemon answered the object %q: %s", selector, item.Meta.Object, err)
		}
		if seen[reference.Path] || reference.Kind == "ccfg" {
			continue
		}
		seen[reference.Path] = true
		targets = append(targets, configValidationTarget{reference: reference, node: firstConfigNode(item.Data)})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].reference.Path < targets[j].reference.Path })

	start := 0
	if options.Cursor != "" {
		start = sort.Search(len(targets), func(i int) bool { return targets[i].reference.Path > options.Cursor })
	}
	end := min(start+maxValidatedObjects, len(targets))
	page := targets[start:end]

	outcomes, throttled := s.validateConfigPage(ctx, page)
	if err := ctx.Err(); err != nil {
		return ObjectConfigValidations{}, err
	}
	// The page ends before the first object not validated: those after it,
	// validated or not, are validated again by the next call.
	cut := len(page)
	for i, o := range outcomes {
		if !o.ran {
			cut = i
			break
		}
	}
	stopped := ""
	if cut < len(page) {
		stopped = stoppedDeadline
		if throttled {
			stopped = stoppedThrottled
		}
		if cut == 0 {
			if throttled {
				return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: the daemon refuses more requests of this MCP for now (HTTP 429): call again in a few seconds", selector)
			}
			return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: no validation answered within %s", selector, configValidationsDeadline)
		}
	}
	page, outcomes = page[:cut], outcomes[:cut]

	result := ObjectConfigValidations{
		Selector: selector, Total: len(targets), Validated: len(page),
		Objects: []ObjectConfigValidationSummary{}, Truncated: start+cut < len(targets), Stopped: stopped,
	}
	if result.Truncated {
		result.NextCursor = page[len(page)-1].reference.Path
	}
	failures := map[string]*ObjectConfigValidationFailure{}
	var firstFailure error
	for i, o := range outcomes {
		path := page[i].reference.Path
		if o.err != nil {
			var problem daemonProblem
			isProblem := errors.As(o.err, &problem)
			switch {
			case isProblem && problem.HTTPStatus() == http.StatusUnauthorized:
				return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: the daemon refused the token: %w", selector, o.err)
			case errors.Is(o.err, client.ErrIncompatible):
				return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: %w", selector, o.err)
			case isProblem && problem.HTTPStatus() == http.StatusForbidden:
				result.RefusedObjects++
				if len(result.Refused) < maxConfigValidationsNamed {
					result.Refused = append(result.Refused, path)
				}
				continue
			}
			if firstFailure == nil {
				firstFailure = o.err
			}
			result.FailedObjects++
			reason := configValidationFailureReason(page[i].node, o.err)
			failure := failures[reason]
			if failure == nil {
				failure = &ObjectConfigValidationFailure{Reason: reason, Paths: []string{}}
				failures[reason] = failure
			}
			failure.Objects++
			if len(failure.Paths) < maxConfigValidationFailurePaths {
				failure.Paths = append(failure.Paths, path)
			}
			continue
		}
		switch o.validation.Level {
		case configValidationError:
			result.ErrorObjects++
		case configValidationWarning:
			result.WarningObjects++
		default:
			result.ValidObjects++
			continue
		}
		result.Objects = append(result.Objects, ObjectConfigValidationSummary{
			Path: path, Node: o.validation.Node, Level: o.validation.Level,
			ErrorCount: o.validation.ErrorCount, WarningCount: o.validation.WarningCount,
			Alerts: o.validation.Alerts,
		})
	}
	if len(page) > 0 && result.FailedObjects == len(page) {
		return ObjectConfigValidations{}, fmt.Errorf("validate the configurations of %s: every validation of the %d objects failed, the first: %w", selector, len(page), firstFailure)
	}
	result.Failed = groupConfigValidationFailures(failures)
	sort.SliceStable(result.Objects, func(i, j int) bool {
		a, b := result.Objects[i], result.Objects[j]
		if a.Level != b.Level {
			return a.Level == configValidationError
		}
		return a.Path < b.Path
	})
	boundListedConfigValidations(result.Objects)
	if len(targets) == 0 {
		result.Note = configValidationsEmptyNote
	}
	if configuration {
		result.TermNote = objectSelectorTermNote
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// configValidationTarget is an object to validate and the node to validate
// it on, "" when none.
type configValidationTarget struct {
	reference ClusterObjectReference
	node      string
}

// validateConfigPage validates the objects of a page, configValidationParallel
// at a time, within configValidationsDeadline. An object is not sent once the
// daemon throttled a request, which it tells, or once maxConfigValidationNodeFailures
// requests did not reach its node: those fail without a request. A validation
// the deadline of the call or a cancellation cut is not ran.
func (s *Service) validateConfigPage(ctx context.Context, page []configValidationTarget) ([]configValidationOutcome, bool) {
	callCtx, cancel := context.WithTimeout(ctx, configValidationsDeadline)
	defer cancel()
	outcomes := make([]configValidationOutcome, len(page))
	slots := make(chan struct{}, configValidationParallel)
	var throttled atomic.Bool
	var mu sync.Mutex
	unreachable := map[string]int{}
	var wait sync.WaitGroup
	for i, t := range page {
		if t.node == "" {
			outcomes[i] = configValidationOutcome{ran: true, err: errors.New("no node holds its configuration, and its scope names none")}
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case slots <- struct{}{}:
			case <-callCtx.Done():
				return
			}
			defer func() { <-slots }()
			if throttled.Load() || callCtx.Err() != nil {
				return
			}
			mu.Lock()
			down := unreachable[t.node] >= maxConfigValidationNodeFailures
			mu.Unlock()
			if down {
				outcomes[i] = configValidationOutcome{ran: true, err: fmt.Errorf("not sent: %d requests did not reach the node", maxConfigValidationNodeFailures)}
				return
			}
			validation, err := s.validateObjectConfigOn(callCtx, t.reference, t.node)
			if err != nil && callCtx.Err() != nil {
				return
			}
			var problem daemonProblem
			isProblem := errors.As(err, &problem)
			if isProblem && problem.HTTPStatus() == http.StatusTooManyRequests {
				throttled.Store(true)
				return
			}
			if err != nil && !isProblem && !errors.Is(err, client.ErrIncompatible) {
				mu.Lock()
				unreachable[t.node]++
				mu.Unlock()
			}
			outcomes[i] = configValidationOutcome{ran: true, validation: validation, err: err}
		}()
	}
	wait.Wait()
	return outcomes, throttled.Load()
}

// configValidationFailureReason is why a validation failed, short enough to
// keep the detail of the daemon, and the same for the objects failing alike:
// the status and detail of the daemon, or the cause of the request without
// the URL naming the object.
func configValidationFailureReason(node string, err error) string {
	var problem daemonProblem
	var request *url.Error
	var reason string
	switch {
	case errors.As(err, &problem):
		detail := strings.TrimSpace(problem.ProblemDetail())
		if detail == "" {
			detail = http.StatusText(problem.HTTPStatus())
		}
		reason = fmt.Sprintf("HTTP %d: %s", problem.HTTPStatus(), detail)
	case errors.As(err, &request):
		reason = request.Err.Error()
	default:
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			err = cause
		}
		reason = err.Error()
	}
	if node != "" {
		reason = "on " + node + ": " + reason
	}
	return boundedConfigValidationField(reason)
}

// groupConfigValidationFailures orders the reasons, the most frequent first,
// and keeps maxConfigValidationFailureGroups of them.
func groupConfigValidationFailures(failures map[string]*ObjectConfigValidationFailure) []ObjectConfigValidationFailure {
	if len(failures) == 0 {
		return nil
	}
	groups := make([]ObjectConfigValidationFailure, 0, len(failures))
	for _, failure := range failures {
		groups = append(groups, *failure)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Objects != groups[j].Objects {
			return groups[i].Objects > groups[j].Objects
		}
		return groups[i].Reason < groups[j].Reason
	})
	if len(groups) > maxConfigValidationFailureGroups {
		groups = groups[:maxConfigValidationFailureGroups]
	}
	return groups
}

// boundListedConfigValidations shares maxConfigValidationsAlerts among the
// objects listed, one each at least, and maxConfigValidationsCommentsBytes of
// comments, in the order of the list.
func boundListedConfigValidations(objects []ObjectConfigValidationSummary) {
	if len(objects) == 0 {
		return
	}
	each := max(1, maxConfigValidationsAlerts/len(objects))
	budget := maxConfigValidationsCommentsBytes
	for i := range objects {
		alerts := objects[i].Alerts
		kept := make([]ConfigValidationAlert, 0, min(len(alerts), each))
		for _, alert := range alerts {
			if len(kept) == each || len(alert.Comment) > budget {
				break
			}
			budget -= len(alert.Comment)
			kept = append(kept, alert)
		}
		objects[i].Alerts, objects[i].AlertsTruncated = kept, len(kept) < len(alerts)
	}
}

// firstConfigNode is the node an object of a list is validated on: the
// first by name of those holding its configuration, else of its scope, ""
// when none.
func firstConfigNode(data daemonObjectData) string {
	nodes := make([]string, 0, len(data.Instances))
	for node := range data.Instances {
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		nodes = append(nodes, data.Scope...)
	}
	if len(nodes) == 0 {
		return ""
	}
	sort.Strings(nodes)
	return nodes[0]
}
