# Preview tools

A client that asks the user to confirm a state-changing call, such as the
OpenSVC AI agent, shows what the call would do. The preview tools tell it.
They read, never write, and are registered whether actions are enabled or
not. They are for the client, never for the model: their `_meta` holds
`opensvc/hidden: true`, which a client honors by never offering them to the
model nor returning them from a tool search, and they carry no search
keywords.

Implementation:

- business logic: `internal/core/preview.go`, `internal/core/config_preview.go`;
- MCP definitions: `internal/tools/preview.go`, and the `_meta` of the tools
  in `internal/tools/search_meta.go`.

## The contract

A tool the user confirms names its preview in its `_meta`:

- `opensvc/preview_tool`: the name of the preview tool;
- `opensvc/preview_wraps_arguments: true`: the preview takes
  `{"tool": <name>, "arguments": <the arguments of the call>}`; without it,
  the preview takes the arguments of the call themselves, the same schema as
  the tool.

Every destructive tool names one; a test holds the table to the tools, so
that a new destructive tool cannot come without its preview.

Every preview returns the same `Preview` result:

- `summary`: `text`, the sentence in English; `key`, its name; `params`, the
  values it names. A client composes the sentence again in its own language
  from `key` and `params`, `text` otherwise. The values come from the
  arguments of the model: untrusted text, to be shown as it is.
- `changes`: the keys set or unset, each with `key` as named, its node `scope`
  after `@`, the `op` (`=`, `+=`, `-=`, `|=`, `^=`, `insert` for an indexed
  `=`, `unset`), the `value` given, `before`, the raw configured value read
  now, not evaluated, `after`, the value the MCP computes, and
  `after_computed_by_mcp`; a `note` says a key not set, a value too long to
  compute from, a change that changes nothing, in English, and `note_keys`
  name its notes for a client to say them in its own language: `not_set`,
  `empty_or_not_set`, `secret`, `applied_after_file`,
  `section_deleted_first`, `current_too_long`, `after_uncomputed`,
  `not_in_list`, `already_in_list`, `empty_index`, `index_past_words`.
- `deleted`: the sections deleted, with the keys they hold, read now, and
  `secrets_omitted`, the secret keys among them, never named.
- `content`: a content written as given, with its `media_type`.
- `read_at`: when the current values were read. They may change before the
  call runs: the update itself takes no condition on them.
- `warnings`: what limits the preview, such as a read cut short.
- `secrets_omitted`: the changes on secret keys, `cluster.secret`,
  `node.uuid`, or the data keys of a `sec` or `usr`: neither their current nor
  their new value is shown, and the secrets are not even read.

The daemon applies the operations and validates the result: it may still
refuse the call, for a value it does not accept, a resource up, or a
configuration changed meanwhile.

## Tools

### `preview_object_config_update`

Takes the arguments of `update_object_config`. Reads the keys the set and
unset operations name, through the read of `get_object_config`, and the whole
configuration, up to 200 keys, when sections are deleted. The changes follow
the order the daemon applies them: sections deleted, keys unset, then each
set from the value the previous operations leave. `after` is computed as om3
applies the operator to the raw value, the value of the operation unquoted
as om3 does, one pair of quotes around it removed when it holds no other
quote, no escape read: `=` sets it, `+=` appends the value
even if present, `-=` removes every word equal to it, `|=` appends it when
absent, `^=` removes it when present or appends it, `<key>[<n>]=<value>`
inserts it at word `n`, an empty value there removing nothing. It is not
computed from a value cut to its 4096 characters, nor for an index past the
words. The keys are matched as the daemon names them, a scoped one with its
`@<node>` in the keyword, for an object as for a node, and a key of `DEFAULT`
without its section: `DEFAULT.orchestrate` and `orchestrate` name one key,
whose current value the preview gives under the name of the operation, and
the second operation on it starts from the first. The section of a key is
told as om3 tells it: the part before the first dot for `env`, `data` and
`labels`, before the last dot ahead of the scope for the others, `DEFAULT`
for a key without a dot, by its exact case: `DEFAULT.a.b` is the key `b` of
a section `DEFAULT.a`, and `default.orchestrate` is not a key of `DEFAULT`.
Summary key
`config_update`, params `path`, `sets`, `unsets`, `deletes`.

### `preview_node_config_update`

Takes the arguments of `update_node_config`, and reads through
`get_node_config_keywords`. Summary key `node_config_update`, params `node`,
`sets`, `unsets`, `deletes`.

### `preview_object_create`

Takes the arguments of `create_object`. Reads nothing: `content` is the
configuration file as given, `text/x-ini`, cut at 64 KiB, and the set
operations applied after it are changes without `before`. Summary key
`object_create`, params `path`, `kind`, `sections`.

### `preview_action`

Takes `{tool, arguments}` and reads nothing: the summary of an action, from a
sentence per tool, such as `Stop {path} on {node}` for `stop_instance`. The
summary key is the tool name; `rids`, when given, ends the sentence in
parentheses, and `to` adds the target of a switch. A tool without a sentence,
or an argument the sentence names missing, is an error.
