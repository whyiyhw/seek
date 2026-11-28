### Topic fanout keyed coarser than the producer unit — cross-project SSE leak
- **Saw**: W3.2's `subagents` topic pushed project A's subagent rows into a stream subscribed for project B (card c_3736yl); every existing test was green — all ran a single project.
- **Why**: subscribers sat in a hub keyed by TOPIC NAME (`f.topics["subagents"]`) while the producer was a per-INDEX-PATH tail-follow (`f.follows[indexPath]`); `followLoop → pushTopic(topic)` fanned out to every subscriber of the name, spanning all projects.
- **Fix**: make the follow the fanout unit — subscriber channels live on `topicFollow.subs`, `followLoop` pushes via `pushFollow(fl, ev)` to its own follow only, and the topic-name hub (`f.topics`) is gone (`len(subs)` replaces the separate `refs` counter). Rework commit on `drive/c_3736yl`.
- **Lesson**: a fanout registry must be keyed at least as finely as its producer unit — coarser keying leaks events across the finer dimension; hang the subscriber list on the producer-side shard, not a parallel name-keyed map.
- **Refs**: `internal/controlplane/webfrontend_topic.go:topicFollow`, `internal/controlplane/webfrontend_topic_test.go:TestWeb_TopicProjectIsolation`
