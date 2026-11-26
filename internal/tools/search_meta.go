package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/core"
)

// The _meta keys the tool search of an AI client reads, such as the OpenSVC
// AI agent: a client searching the catalog always shows the core tools, and
// ranks the others with their keywords besides their name, title,
// description and parameters. A client that does not search ignores them.
const (
	coreToolMeta     = "opensvc/core"
	keywordsToolMeta = "opensvc/keywords"
	// A client hides a tool marked hidden from the model, and from its search.
	hiddenToolMeta = "opensvc/hidden"
	// previewToolMeta names the tool a client calls, before it asks the user
	// to confirm a call, to tell what the call would do: with the arguments
	// of the call, or, previewWrapsMeta set, with {tool, arguments}.
	previewToolMeta  = "opensvc/preview_tool"
	previewWrapsMeta = "opensvc/preview_wraps_arguments"

	maxToolKeywords     = 12
	maxToolKeywordBytes = 40
)

// coreTools are always shown to the model: the state of the cluster, of its
// objects, instances and nodes, and the identity of the caller. The other
// tools are found by a search.
var coreTools = []string{"get_cluster_status", "list_cluster_objects", "get_object_status", "get_instance_status", "get_node_status", "get_caller_identity"}

// previewTools are the tools that tell what a write would do with its own
// arguments; the actions with a sentence are told by previewActionTool.
var previewTools = map[string]string{
	"update_object_config": "preview_object_config_update",
	"update_node_config":   "preview_node_config_update",
	"create_object":        "preview_object_create",
}

const previewActionTool = "preview_action"

// hiddenTools are for the client, never offered to the model.
var hiddenTools = []string{"preview_object_config_update", "preview_node_config_update", "preview_object_create", previewActionTool}

// toolKeywords are the words a question may use for a tool that its
// description does not hold, such as synonyms: the model does not see them,
// they only rank the tool in a search.
var toolKeywords = map[string][]string{
	"abort_cluster_orchestration":    {"abort cluster", "cancel cluster freeze", "stop cluster orchestration"},
	"abort_node":                     {"abort drain", "cancel drain", "stop node orchestration", "interrupt node"},
	"abort_object_orchestration":     {"abort", "cancel", "stuck orchestration", "interrupt", "target state"},
	"cap_object":                     {"limit", "cgroup", "cpu limit", "memory limit", "quota", "throttle"},
	"clear_instance":                 {"clear", "start failed", "reset failure", "monitor error", "unblock instance"},
	"clear_node":                     {"drain failed", "freeze failed", "reset node failure", "node monitor error"},
	"create_object":                  {"new service", "new object", "define service", "add service", "deploy", "object template"},
	"create_object_certificate":      {"create certificate", "generate tls certificate", "self-signed", "sign certificate", "x509"},
	"delete_object":                  {"remove", "delete service", "drop object", "destroy config", "clean up"},
	"delete_object_data_key":         {"delete key", "remove secret", "remove config file", "drop key"},
	"dequeue_node_actions":           {"dequeue", "collector queue", "run queued actions", "file du collector", "pending actions now"},
	"disable_object":                 {"disable", "deactivate", "turn off resource", "exclude resource"},
	"drain_node":                     {"drain", "evacuate node", "node maintenance", "empty node", "shutdown all instances"},
	"enable_object":                  {"enable", "activate", "turn on resource", "reenable"},
	"freeze_cluster":                 {"freeze cluster", "global maintenance", "stop failover everywhere", "pause orchestration"},
	"freeze_instance":                {"freeze node instance", "maintenance on one node", "pause instance"},
	"freeze_node":                    {"freeze node", "node maintenance", "suspend node automation", "pause node"},
	"freeze_object":                  {"freeze", "maintenance", "disable failover", "suspend automation", "pause"},
	"get_caller_identity":            {"whoami", "who am i", "permissions", "roles", "grants", "access rights"},
	"get_cluster_config":             {"cluster.conf", "cluster settings", "cluster configuration", "cluster config"},
	"get_cluster_status":             {"cluster overview", "cluster health", "heartbeat", "quorum", "nodes state"},
	"get_container_logs":             {"docker logs", "podman logs", "container output", "stdout", "stderr"},
	"get_daemon_log_level":           {"log level", "daemon verbosity", "journald level"},
	"get_instance_logs":              {"instance log", "orchestration log", "why failed", "action log", "start failed", "cause of a failure", "error message", "pourquoi en échec"},
	"get_instance_resource_file":     {"resource file", "replicated file", "kvm xml", "domain definition", "sub_devs", "lxc config"},
	"get_instance_status":            {"instance details", "resources status", "monitor state", "instance health"},
	"get_node_config":                {"node.conf", "node settings", "node configuration"},
	"get_node_config_keywords":       {"node setting", "node keyword", "node.conf value", "heartbeat setting", "node env", "collector id", "node uuid", "collector identifier"},
	"get_node_daemon_metrics":        {"prometheus", "metrics", "counters", "daemon performance", "monitoring"},
	"get_node_drbd_config":           {"drbd", "drbd resource", ".res file", "replication config", "drbd.d"},
	"get_node_logs":                  {"journal", "journalctl", "daemon log", "node log", "errors", "session id"},
	"get_node_san_topology":          {"san", "fibre channel", "iscsi", "hba", "initiator", "target", "multipath", "storage paths"},
	"get_node_status":                {"node health", "node details", "load", "memory", "labels", "node state"},
	"get_object_config":              {"service configuration", "object definition", "object keywords", "service config", "cluster config", "cluster keyword"},
	"get_object_data_key":            {"read config file", "configmap value", "cfg key value", "show key"},
	"get_object_status":              {"service status", "is it up", "placement", "where it runs", "object health"},
	"get_relay_status":               {"relay", "heartbeat relay", "relais", "relay slots", "hb relay status"},
	"giveback_object":                {"giveback", "preferred node", "rebalance", "move back", "placement"},
	"list_cluster_ip_resources":      {"vip", "virtual ip", "service ip", "ip resource", "addresses"},
	"list_cluster_locks":             {"cluster lock", "locks", "lease", "ipam lock", "who holds the lock", "verrou", "lock held", "locked"},
	"list_clusters":                  {"clusters", "cluster id", "catalog", "which cluster"},
	"list_cluster_objects":           {"list services", "list objects", "selector", "namespaces", "inventory"},
	"list_daemon_executions":         {"running commands", "executions", "processes", "in progress", "session"},
	"list_daemon_orchestrations":     {"orchestration in progress", "running orchestration", "pending actions", "orchestration id"},
	"list_dns_records":               {"dns", "name resolution", "zone", "records", "hostname"},
	"list_instance_sync_snapshots":   {"snapshot", "backup", "restore point", "sync store", "sauvegarde", "instantané"},
	"list_network_ips":               {"network addresses", "allocated ip", "used ip", "ipam", "backend network ip", "addresses used", "who uses"},
	"list_networks":                  {"backend network", "subnet", "address range", "free addresses"},
	"list_node_capabilities":         {"capabilities", "features", "supported", "drivers available"},
	"list_node_config_keywords":      {"node keywords reference", "node config syntax", "heartbeat options", "arbitrator options"},
	"list_node_disks":                {"disk", "lun", "wwid", "storage", "block device", "volume"},
	"list_node_drivers":              {"drivers", "plugins", "resource types", "supported drivers", "driver features", "driver availability", "driver grant"},
	"list_node_hardware":             {"hardware", "pci", "memory banks", "cpu", "inventory", "devices"},
	"list_node_ip_addresses":         {"node ip", "interfaces", "nic", "addresses", "network interfaces"},
	"list_node_locks":                {"node locks", "lock holders", "speaker 503", "verrous du nœud"},
	"list_node_network_reservations": {"ip reservation", "reserved address", "ip allocation", "réservation ip", "address pool"},
	"list_node_packages":             {"package", "rpm", "deb", "software", "version", "installed"},
	"list_node_properties":           {"properties", "asset", "serial", "model", "os", "kernel"},
	"list_object_config_keywords":    {"keyword reference", "supported options", "configuration syntax", "driver options", "cluster keyword"},
	"list_object_data_keys":          {"secret keys", "secret", "configmap", "data keys", "sec", "cfg"},
	"list_object_instances":          {"instances", "per node", "instance state", "monitor", "availability"},
	"list_object_resources":          {"resources", "resource status", "fs", "ip", "container", "disk"},
	"list_pool_volumes":              {"volumes", "orphan volumes", "pool usage", "claims"},
	"list_resource_info":             {"resource info", "key value", "driver details", "cached information"},
	"list_schedules":                 {"schedule", "cron", "scheduled tasks", "next run", "last run"},
	"list_storage_pools":             {"storage pool", "capacity", "free space", "pool usage", "thin provisioning"},
	"mark_instance_unprovisioned":    {"state only", "mark unprovisioned", "without touching", "déjà libéré", "freed by hand", "update provisioned state"},
	"probe_node_reachability":        {"ping", "reachable", "connectivity", "node answers", "health check"},
	"provision_object":               {"provision", "create storage", "format", "deploy", "allocate", "first start"},
	"purge_object":                   {"purge", "remove provisioned", "destroy service", "decommission", "wipe object", "delete data"},
	"refresh_instance_status":        {"refresh", "update status", "status probe", "resync status"},
	"refresh_node_capabilities":      {"rafraîchir", "cache périmé", "rescan capabilities", "waiting for cached value", "driver installed"},
	"refresh_node_disks":             {"rafraîchir", "cache périmé", "push disks", "disk inventory", "nouveau lun", "new lun"},
	"refresh_node_hardware":          {"rafraîchir", "cache périmé", "push asset", "hardware inventory", "stale inventory", "waiting for cached value"},
	"refresh_node_packages":          {"rafraîchir", "cache périmé", "push pkg", "package inventory", "stale packages"},
	"refresh_resource_info":          {"rafraîchir", "cache périmé", "resource info refresh", "stale resource info", "recompute info"},
	"renew_object_certificate":       {"renew certificate", "acme", "letsencrypt", "certificate expiry", "rotate certificate"},
	"reset_instance_caps":            {"remove limits", "uncap", "pg reset", "lift cgroup limits", "reset cgroup"},
	"resize_object":                  {"grow", "extend volume", "enlarge", "increase size", "expand"},
	"restart_daemon":                 {"restart daemon", "restart agent", "reload daemon", "bounce om"},
	"restart_daemon_listener":        {"restart listener", "restart api", "rebind listener", "https listener"},
	"restart_heartbeat":              {"restart heartbeat", "heartbeat stuck", "reset hb", "heartbeat failed"},
	"restart_instance":               {"restart on node", "bounce instance", "restart one node"},
	"restart_object":                 {"restart", "reboot service", "bounce", "start again"},
	"run_task":                       {"run task", "lancer une tâche", "backup job", "execute job", "run script", "trigger job", "task"},
	"scan_node_scsi":                 {"scsi scan", "nouveau lun", "new lun", "rescan disks", "san lun mapped", "hba"},
	"set_daemon_log_level":           {"set log level", "quiet daemon", "reduce logs", "daemon verbosity"},
	"set_object_data_key":            {"set key", "add secret", "store password", "add config file", "change secret", "user password"},
	"shutdown_instance":              {"shutdown", "node shutdown", "stop standby", "before reboot", "full stop on node", "reboot", "arrêt propre", "avant reboot", "maintenance du nœud", "kernel update", "stop cleanly", "stop all resources"},
	"start_heartbeat":                {"start heartbeat", "enable hb stream", "resume heartbeat"},
	"start_instance":                 {"start on node", "bring up instance", "start one node", "local start"},
	"start_instance_standby":         {"standby", "start standby", "passive node", "standby resources", "stdby", "ressources de secours", "secours", "remonter secours"},
	"start_object":                   {"start", "bring up", "boot service", "provision start"},
	"stop_instance":                  {"stop on node", "bring down instance", "stop one node", "local stop"},
	"stop_object":                    {"stop", "shutdown", "bring down", "halt service"},
	"switch_object":                  {"switch", "relocate", "move", "failover", "migrate", "takeover"},
	"unfreeze_cluster":               {"unfreeze cluster", "thaw cluster", "end global maintenance", "resume orchestration"},
	"unfreeze_instance":              {"unfreeze node instance", "thaw instance", "resume instance"},
	"unfreeze_node":                  {"unfreeze node", "end node maintenance", "resume node", "thaw node"},
	"unfreeze_object":                {"unfreeze", "thaw", "resume automation", "end maintenance"},
	"unprovision_instance":           {"unprovision on node", "deprovision instance", "local unprovision", "free node storage", "free storage on node", "libérer le stockage", "release disk on node"},
	"unprovision_object":             {"unprovision", "deprovision", "free storage", "destroy data", "release resources", "teardown"},
	"update_instance_caps":           {"apply limits", "cgroup", "pg update", "memory limit on node", "cpu limit on node"},
	"update_node_config":             {"set node setting", "change node config", "edit node.conf", "configure heartbeat", "set node env"},
	"update_object_config":           {"edit config", "change setting", "set keyword", "modify configuration", "configure", "unset", "set cluster", "cluster config"},
	"validate_object_config":         {"lint", "typo", "syntax check", "check the config", "why is the config in error", "is the config valid", "bad keyword", "config errors"},
}

// searchMeta is the _meta of a tool for the tool search. Every tool has
// keywords, so that a new tool is not registered without them.
func searchMeta(name string) (mcp.Meta, error) {
	if slices.Contains(hiddenTools, name) {
		return mcp.Meta{hiddenToolMeta: true}, nil
	}
	keywords, ok := toolKeywords[name]
	if !ok || len(keywords) == 0 || len(keywords) > maxToolKeywords {
		return nil, fmt.Errorf("tool %q needs 1 to %d search keywords", name, maxToolKeywords)
	}
	for _, keyword := range keywords {
		if keyword == "" || len(keyword) > maxToolKeywordBytes || keyword != strings.ToLower(strings.TrimSpace(keyword)) {
			return nil, fmt.Errorf("tool %q search keyword %q must be lowercase text of at most %d bytes", name, keyword, maxToolKeywordBytes)
		}
	}
	meta := mcp.Meta{keywordsToolMeta: slices.Clone(keywords)}
	if slices.Contains(coreTools, name) {
		meta[coreToolMeta] = true
	}
	if preview, ok := previewTools[name]; ok {
		meta[previewToolMeta] = preview
	} else if slices.Contains(core.ActionSummaryTools(), name) {
		meta[previewToolMeta] = previewActionTool
		meta[previewWrapsMeta] = true
	}
	return meta, nil
}
