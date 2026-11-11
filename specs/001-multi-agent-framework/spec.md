# Feature Specification: Zamnius Multi-Agent Framework

**Feature Branch**: `001-multi-agent-framework`

**Created**: 2026-11-11

**Status**: Draft

**Grilled on**: 2026-11-11

**Input**: User description: "docs/spec-draft.md — build a production-grade multi-agent framework named Zamnius in Go: orchestration of specialized agents, a workflow engine with phases and fan-out/join, rich memory systems, messaging, persistence, and full resumability, exposed through CLI, HTTP and gRPC."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Run an ordered multi-phase workflow with parallel branches (Priority: P1)

An engineer describes a workflow as an ordered set of phases — for example *plan → implement in parallel → test → review*. They start a run, and the framework drives the specialized agents through each phase in order, executes parallel branches concurrently, joins their results, and collects the artifacts each phase produced. Budgets, retries, and timeouts keep the run bounded.

**Why this priority**: This is the entry point of the whole product. Without a working phase engine with fan-out/join, no other capability (resume, audit, memory) has anything to operate on. It alone delivers value: task automation with concurrency.

**Independent Test**: A workflow with one single-agent phase, one three-way parallel phase, and one join phase can be started from scratch and completed end-to-end, producing the expected artifacts in the expected order, with the parallel phase measurably faster than a sequential equivalent.

**Acceptance Scenarios**:

1. **Given** a workflow definition with four ordered phases, **When** the run starts, **Then** each phase executes only after the preceding phase reports a terminal outcome, and each phase's artifacts are recorded before the next phase begins.
2. **Given** a phase that fans out to three branches, **When** the branches execute, **Then** all three run concurrently, their results are joined according to the declared join rule, and the join output is a single aggregated artifact.
3. **Given** a phase with a declared retry limit of two, **When** an attempt fails twice and succeeds on the third, **Then** the phase succeeds, all attempts are recorded, and downstream phases run exactly once.
4. **Given** a phase whose declared time or budget allowance is exhausted, **When** the allowance is reached, **Then** the phase stops, the run applies the declared failure policy (abort, continue, or escalate), and the reason is recorded.
5. **Given** a phase whose join rule requires all branches, **When** one branch fails permanently, **Then** the join does not produce a partial result and the phase reports a failure naming the failed branch.

---

### User Story 2 - Resume any interrupted run from a fresh instance (Priority: P2)

A run is interrupted — the process is killed, the machine restarts, or the model provider becomes unavailable. Later, a completely new instance of the framework, driven by a different agent and possibly a different model, with no memory of the earlier conversation, is pointed at the same persisted run. It determines exactly where work stopped, what already succeeded, and continues without redoing finished work or duplicating side effects.

**Why this priority**: Recoverability is the highest-priority principle of the project (both for the product and for the development process). A workflow engine that loses progress on interruption is unusable for real engineering work, so this ships alongside P1.

**Independent Test**: Start a run, interrupt it at a chosen point (during a parallel branch, between phases, and mid-artifact-write), then resume from a brand-new instance that has read only the persisted artifacts; the run completes with results equivalent to an uninterrupted run and with no duplicated side effects.

**Acceptance Scenarios**:

1. **Given** a run interrupted during a parallel phase, **When** a fresh instance resumes it, **Then** completed branches are not re-executed, unfinished branches are executed, and the join completes with the full set of results.
2. **Given** a run interrupted after a side-effecting action was performed and recorded, **When** the run resumes, **Then** that action is not repeated.
3. **Given** a run interrupted while an artifact was being written, **When** the run resumes, **Then** the artifact is either fully present or fully absent, never partially readable.
4. **Given** a completed run, **When** resume is requested, **Then** no work is executed and the framework reports the run as already complete.
5. **Given** two instances attempt to resume the same run concurrently, **When** both start, **Then** exactly one becomes the owner and the other is refused without corrupting the run state.
6. **Given** persisted state produced by an older framework version, **When** a newer instance resumes it, **Then** the state is upgraded with a recorded migration or the run is refused with an actionable diagnostic — never silently misinterpreted.
7. **Given** a step attempt interrupted before its outcome was committed, **When** the run resumes, **Then** the attempt restarts from the phase's recorded inputs, the restart is recorded with its reason, and any side effect already recorded as completed is not repeated.

---

### User Story 3 - Reconstruct what happened and why from the audit record (Priority: P3)

A reviewer, compliance officer, or debugging engineer who was not present during the run inspects the append-only event log, decision records, and artifacts, and reconstructs the full timeline: which agent did what, when, with which inputs, why a branch was retried, which tool produced which output, and what was ultimately accepted.

**Why this priority**: Auditability is a mandatory quality attribute and the practical proof of recoverability. Without it, resume claims cannot be verified and trust in autonomous work is impossible.

**Independent Test**: Take a completed run with at least one retry and one escalation, hand only its persisted artifacts to a reader with no prior context, and check that the reader can state the ordered timeline, the cause of each retry, and the final accepted result.

**Acceptance Scenarios**:

1. **Given** a completed run, **When** its audit record is read in order, **Then** every phase transition is present with timestamp, actor, phase, and outcome.
2. **Given** a phase that was retried, **When** its decision record is read, **Then** the recorded reason identifies the triggering condition.
3. **Given** a record that has been written, **When** a later step tries to modify or delete it, **Then** the attempt is rejected and the record remains unchanged.
4. **Given** an artifact referenced by the audit record, **When** the record is read later, **Then** the artifact can be retrieved and verified against its recorded fingerprint.

---

### User Story 4 - Let specialized roles collaborate through a task board and messaging (Priority: P4, follow-on increment)

An orchestrator decomposes work, a project manager keeps the task board and priorities, a planner produces the plan, researchers gather context, coders implement, and testers verify. They coordinate by claiming tasks from a shared board and by sending each other messages, without stepping on each other's work or acting outside their role.

**Why this priority**: Collaboration is what turns a workflow engine into a multi-agent framework, and it is the prerequisite for realistic engineering workflows.

**Independent Test**: A non-trivial task decomposed into five board items is completed by three distinct role agents that claim items, exchange at least one message, and report evidence — with no item executed twice and no role acting outside its permitted scope.

**Acceptance Scenarios**:

1. **Given** a board item that is unclaimed, **When** two agents attempt to claim it at the same time, **Then** exactly one claim succeeds and the other receives the current owner.
2. **Given** an agent that holds a claim, **When** it completes the item with evidence, **Then** the item is marked done with the evidence attached and the claim is released.
3. **Given** a direct message between two agents, **When** delivery is retried after a transient failure, **Then** the recipient observes the message exactly once and in send order per sender–recipient pair.
4. **Given** an agent attempting an action outside its role's permitted scope, **When** it requests the action, **Then** the request is refused, recorded, and the run's failure policy applies.
5. **Given** a delegation from one agent to another, **When** the delegate acts, **Then** the audit record attributes the work to the delegate and the delegation chain is visible.

---

### User Story 5 - Carry knowledge across sessions instead of re-deriving it (Priority: P5, follow-on increment)

Agents keep session-scoped short-term context and a durable long-term memory. Before acting, an agent retrieves only the relevant decisions, constraints, and prior findings instead of replaying the entire history. Repeated or near-duplicate requests are answered from a semantic cache, and long context is safely compressed while pinned constraints survive intact.

**Why this priority**: Context is the scarcest resource in multi-agent work. Memory makes long-running and repeated work affordable and consistent, but it is only useful once there is a workflow and a collaboration model to feed it.

**Independent Test**: Run a session that records a decision, end it, start a new session with no conversation history, and check that the decision is recalled on a related request; then issue a paraphrased repeat request and check the cached answer is reused while pinned constraints still appear in the prompt.

**Acceptance Scenarios**:

1. **Given** a decision recorded in an earlier session, **When** a new session asks a related question, **Then** the decision is returned among the top results with its source reference.
2. **Given** a memory store containing entries from two projects, **When** a session scoped to one project retrieves, **Then** entries from the other project are not returned.
3. **Given** a context that exceeds the configured allowance, **When** compression runs, **Then** the prompt shrinks by at least half while every pinned constraint is still present.
4. **Given** a request semantically equivalent to a previously answered one, **When** it is issued, **Then** the cached answer is returned without recomputation and is marked as cached.
5. **Given** a memory entry that has been superseded, **When** retrieval runs, **Then** the superseded entry is not presented as current, but remains traceable.

---

### User Story 6 - Give agents controlled access to tools, external tool servers, and isolated workspaces (Priority: P6, follow-on increment)

Agents act on the world only through a governed tool registry: code search, file edits, test execution, version-control operations, and external tool servers discovered at runtime. Every capability carries a declared permission scope, an input contract, a timeout, and full recording of inputs and outputs. Concurrent agents work in isolated workspaces so their changes never collide.

**Why this priority**: Tools are how work actually gets done, and uncontrolled tools are how autonomous systems cause damage. Isolation and governance must exist before broad tool access is enabled.

**Independent Test**: Configure a registry with an allow-listed tool, a denied tool, and one external tool server; run an agent that invokes the allowed tool, attempts the denied one, and calls the external tool — and verify outcomes, refusals, isolation of two parallel workspaces, and complete audit records.

**Acceptance Scenarios**:

1. **Given** a tool that is not in the agent's permitted scope, **When** the agent invokes it, **Then** the invocation is refused before any side effect occurs and the refusal is recorded.
2. **Given** two agents working in separate isolated workspaces, **When** both modify the same file path, **Then** neither change is visible to the other until an explicit integration step.
3. **Given** an external tool server that returns an error, **When** the agent invokes it, **Then** the error is surfaced to the agent as a tool result, retries follow the declared policy, and the run continues or fails per policy.
4. **Given** a version-control operation performed through a tool, **When** it completes, **Then** the operation, its target revision, and its result are recorded in the audit trail.
5. **Given** a tool that exceeds its declared timeout, **When** the timeout elapses, **Then** the invocation is terminated, the phase is not left hanging, and the termination is recorded.

---

### User Story 7 - Drive every capability from the command line, over HTTP, over gRPC, or as an embedded library (Priority: P7, follow-on increment)

A single operator runs a workflow from a terminal. A service integrates the framework over a network interface. An application embeds it as a library. All three see the same capabilities, the same configuration semantics, and the same audit records.

**Why this priority**: The product must be adoptable in different deployment shapes, but surfaces are only worth building once the underlying capabilities exist.

**Independent Test**: Perform the same core operation set (start run, inspect status, list artifacts, resume, cancel) through each surface and confirm identical results and records.

**Acceptance Scenarios**:

1. **Given** the same core operation, **When** it is invoked through any of the four surfaces, **Then** the outcome and the resulting audit records are equivalent.
2. **Given** a configuration file in any supported format, **When** the framework starts, **Then** the same effective configuration results, and conflicting duplicates are resolved by a documented precedence.
3. **Given** an invalid configuration, **When** startup is attempted, **Then** startup is refused with a message naming the offending setting and the accepted values.
4. **Given** a long-running operation invoked over a network surface, **When** the client disconnects, **Then** the run continues under the declared ownership policy and the client can reattach to observe it.

---

### User Story 8 - Extend the framework and interoperate with other agent ecosystems (Priority: P8, follow-on increment)

A team adds a custom role, a custom tool, or a custom memory backend without forking the core. Prompt templates are versioned so a behaviour change can be traced to a prompt revision. Agents can discover and delegate to agents outside the process through the A2A protocol, and user-facing applications can observe a live run through an event stream.

**Why this priority**: Extensibility and interoperability are what make the framework open-source-viable and future-proof, but they depend on stable core contracts established by earlier stories.

**Independent Test**: Register a custom role, a custom tool, and a custom memory backend through public contracts; run a workflow that uses them; confirm prompt revisions are recorded per action and that an external agent and a user-facing stream can both participate.

**Acceptance Scenarios**:

1. **Given** a custom role declared through the public contract, **When** a workflow references it, **Then** it runs with its own instructions, permitted tools, and recorded identity.
2. **Given** a prompt template that is revised, **When** an agent acts after the revision, **Then** the action records which prompt version was used, and the prior version remains retrievable.
3. **Given** an external agent reachable over the agent-to-agent protocol, **When** the orchestrator delegates work to it, **Then** discovery, task delegation, progress, and result follow the protocol and appear in the audit trail.
4. **Given** a user-facing application subscribed to a live run, **When** phases and agent activity progress, **Then** the application receives ordered events sufficient to render current state without polling internal storage.

---

### Edge Cases

- **Crash during a state-changing write**: state must be recoverable to the last committed step; no partially applied step is ever resumed as if complete.
- **Resume after the model provider changed**: a resumed run must not depend on provider-specific continuation tokens that are unavailable; the next action must be derivable from persisted intent.
- **Two owners for one run**: a stale ownership claim (crashed owner) must expire and be reclaimable, with the takeover recorded.
- **Budget exhausted mid-branch**: parallel siblings must be stopped consistently, partial outputs preserved as artifacts, and the run's policy applied once.
- **Duplicate side effect on retry**: an action that already succeeded but whose acknowledgement was lost must not execute twice.
- **Untrusted content in inputs or tool output**: content that contains instructions must not be able to expand an agent's permissions or escape its workspace.
- **Model returns unstructured or invalid output where structure was required**: the phase must fail predictably per policy rather than persist malformed artifacts.
- **A role referenced by a persisted run no longer exists**: the run pauses with a diagnostic rather than guessing a substitute.
- **Workspace conflict during integration** (including a git worktree merge conflict): the conflict is surfaced to the owning agent, not auto-resolved destructively.
- **Disk full or store unavailable**: the run halts in a consistent state and reports that writes are failing; it must not continue while unable to record.
- **Clock differences between hosts**: ordering must rely on recorded sequence, not wall-clock comparisons alone.
- **Cancel during a parallel join**: all branches observe cancellation, no branch is left orphaned, and the run ends in a terminal state.

## Requirements *(mandatory)*

### Functional Requirements

#### Workflow orchestration

- **FR-001**: The system MUST allow a workflow to be defined as an ordered sequence of phases.
- **FR-002**: A phase MUST be able to execute either a single agent or a fan-out of multiple agents in parallel.
- **FR-003**: A fan-out phase MUST support a declared join rule (all, any, or a specified number of successful branches) with deterministic aggregation of branch results.
- **FR-004**: Each phase MUST record the artifacts it produces, and downstream phases MUST be able to reference upstream artifacts.
- **FR-005**: Each phase MUST support a declared retry limit, a declared timeout, and declared budgets covering attempt count, wall-clock time, and token consumption.
- **FR-006**: Each phase MUST support a declarable failure policy: abort the run, continue with a recorded degraded outcome, or escalate for a decision.
- **FR-007**: The system MUST stop work when a declared budget is exhausted and record which allowance was exhausted and where. Budgets MUST be declarable at both run level and phase level, and MUST cover attempt count, wall-clock time, and token consumption.
- **FR-008**: The system MUST allow a run to be cancelled, and cancellation MUST terminate all active branches before the run reaches a terminal state.
- **FR-009**: The system MUST support conditional progression between phases (a phase may be skipped or repeated based on a declared condition and its recorded outcome).
- **FR-010**: The system MUST record, for every phase transition, the actor, the phase, the timestamp, the outcome, and the cause.
- **FR-061**: A workflow definition MUST be expressible as a declarative document in the supported configuration formats, reviewable and versionable outside the running process, and MUST also be constructible programmatically through the library. Both paths MUST pass through the same validation and produce the same in-memory definition.

#### Model access

- **FR-058**: The system MUST define a provider-neutral model access contract covering prompt input, structured result, token usage reporting, cancellation, and error classification. Provider-specific behaviour MUST live behind that contract.
- **FR-059**: The system MUST ship at least one working reference adapter so a workflow can run end to end, and MUST allow the provider in use to be replaced by configuration without changing workflow definitions.

#### Access surfaces for this increment

- **FR-060**: The system MUST expose the committed increment as an embedded library plus a minimal command-line surface that can start a run, resume a run, report status, and list artifacts. HTTP and gRPC surfaces are delivered by the follow-on increment covering story 7.

#### Recoverability

- **FR-011**: The system MUST persist run state after every state-changing step, such that an interrupted run can be resumed from the last committed step. Each state change and its corresponding event record MUST commit atomically, so the audit trail and the run state can never disagree.
- **FR-012**: Resume MUST be possible by an instance that has read only persisted artifacts and has no prior conversation context.
- **FR-013**: Resume MUST NOT re-execute steps that already reached a terminal outcome.
- **FR-014**: Resume MUST NOT repeat side-effecting actions that are recorded as completed.
- **FR-015**: Artifact writes MUST be atomic: an artifact is either fully present and verifiable or absent.
- **FR-016**: The system MUST ensure at most one instance owns a run at a time, and MUST support reclaiming ownership from a failed owner with a recorded takeover. Ownership MUST be enforced between processes on the same host that share the durable store; coordination across separate hosts is out of scope for this increment.
- **FR-017**: Persisted run state MUST be versioned, and resuming state written by an incompatible version MUST either migrate with a recorded migration or be refused with an actionable diagnostic.
- **FR-063**: A step attempt MUST be atomic with respect to persistence: its outcome either commits wholly or the attempt is treated as not having happened. On resume, an uncommitted attempt MUST restart from the phase's recorded inputs, the restart MUST be recorded with its cause, and side-effecting actions MUST carry idempotency keys so a repeated attempt cannot duplicate them.
- **FR-018**: Resume of an already-completed run MUST be an idempotent no-op that reports completion.

#### Audit and observability

- **FR-019**: The system MUST maintain an append-only event log with a total order per run.
- **FR-020**: The system MUST record decision records that capture why a non-trivial choice was made (including retries, escalations, and branch selections).
- **FR-021**: Recorded events and decision records MUST be immutable once written.
- **FR-022**: The system MUST emit structured logs, traces, and metrics suitable for production diagnosis.
- **FR-023**: The system MUST allow a reader with no prior context to reconstruct, from persisted records alone, the current state of a run and its next expected action.

#### Agents in this increment

- **FR-062**: An agent MUST be identified by a stable name and carry declared instructions, a model selection, and its own budgets. Role permission scoping, the task board, and messaging are delivered by the follow-on collaboration increment, so this increment MUST NOT require them to run a workflow.

#### Roles and collaboration (follow-on increment, story 4)

- **FR-024**: The system MUST provide the built-in roles Orchestrator, Project Manager, Planner, Researcher, Coder, and Tester, each with declared responsibilities.
- **FR-025**: Each role MUST declare the tools and actions it is permitted to use, and requests outside that scope MUST be refused and recorded.
- **FR-026**: The system MUST provide a shared task board supporting item creation, claiming, blocking, completion with evidence, and release.
- **FR-027**: Task claim MUST be exclusive: concurrent claims MUST resolve to exactly one owner.
- **FR-028**: The system MUST provide messaging between agents with addressing, per-pair ordering, and exactly-once observation by the recipient under retries.
- **FR-029**: The system MUST support delegating work between agents while preserving attribution and the delegation chain in the audit record.
- **FR-030**: The system MUST support workflow-scoped shared state that all participating agents can read and write under recorded access control.

#### Memory and context (follow-on increment, story 5)

- **FR-031**: The system MUST provide session-scoped short-term memory and durable long-term memory with distinct lifecycles. A session is the context thread of one agent instance, and one session's short-term memory MUST NOT be readable by another session.
- **FR-032**: Retrieval MUST return relevant entries with source references and MUST support scope restrictions (for example, project scope).
- **FR-033**: The system MUST support semantic search over remembered content and MUST keep the storage backend replaceable.
- **FR-034**: The system MUST provide a semantic cache that serves semantically equivalent repeated requests without recomputation and can be invalidated.
- **FR-035**: The system MUST compress oversized context while preserving explicitly pinned facts and constraints.
- **FR-036**: The system MUST record supersession of outdated memory so that stale entries are not presented as current while remaining traceable.
- **FR-037**: The system MUST represent relationships between memories and between workflow steps as a graph that can be traversed for retrieval and audit.

#### Prompt management

- **FR-038**: The system MUST manage prompts as versioned templates with sections, variables, and constraints.
- **FR-039**: Every agent action MUST record which prompt template version was used, and prior versions MUST remain retrievable.
- **FR-040**: Prompt changes MUST be attributable and auditable, and MUST NOT silently alter the behaviour of an in-flight run.

#### Tools and integration (follow-on increment, story 6)

- **FR-041**: The system MUST provide a tool registry where each tool declares its input contract, permission scope, timeout, and whether it has side effects.
- **FR-042**: Tool invocations MUST be recorded with inputs, outputs, duration, and outcome, with sensitive values redacted.
- **FR-043**: The system MUST support connecting to external tool servers via the Model Context Protocol, including discovery and invocation.
- **FR-044**: The system MUST provide built-in version-control tools (status, diff, commit, branch, and related operations).
- **FR-045**: The system MUST support isolated workspaces so concurrent agents do not interfere, with an explicit integration step.
- **FR-046**: The system MUST treat external content (files, tool output, retrieved documents) as untrusted and MUST prevent it from granting permissions or executing without an explicit decision.
- **FR-047**: The system MUST expose a stable public contract for custom tools, custom roles, custom memory backends, and custom agents.

#### Interoperability (follow-on increment, story 8)

- **FR-048**: The system MUST support agent-to-agent interoperability for discovery, task delegation, progress reporting, and result exchange.
- **FR-049**: The system MUST provide an ordered event stream that user-facing applications can subscribe to for live run state.

#### Access surfaces and configuration (story 7; this increment delivers FR-060 only)

- **FR-050**: The system MUST expose the same core capabilities through a command-line interface, an HTTP interface, a gRPC interface, and an embedded library usage mode.
- **FR-051**: The system MUST support configuration in YAML, JSON, and TOML with a documented precedence order and validation at startup.
- **FR-052**: The system MUST refuse to start on invalid configuration, naming the offending setting and accepted values.
- **FR-053**: Long-running operations invoked over a network surface MUST survive client disconnection and permit reattachment.

#### Security and operations

- **FR-054**: Secrets MUST be referenced indirectly (by name or handle) and MUST NOT appear in artifacts, prompts, logs, or event records.
- **FR-055**: Enforcement of permission scopes MUST occur before any side effect is performed.
- **FR-056**: The system MUST provide a durability policy per store such that a run halts rather than continue when it cannot record state.
- **FR-057**: The system MUST allow the persistence backend to be selected by configuration without changing workflow definitions.

### Key Entities *(include if data involved)*

- **Workflow Definition**: The declared ordered phases, their agents or fan-out sets, join rules, budgets, retries, timeouts, and failure policies.
- **Run**: One execution of a workflow definition; carries lifecycle state, ownership, current position, and terminal outcome.
- **Phase**: An ordered step within a run; single-agent or fan-out; holds policy settings and its outcome.
- **Branch**: One parallel lane inside a fan-out phase. Distinct from a git branch, which is always named as such.
- **Step Attempt**: One agent execution at a step, with its attempt number, inputs, prompt version, tool activity, and outcome. An attempt commits atomically: it either produced a recorded outcome or it is treated as not having happened.
- **Artifact**: A produced output (plan, diff, evidence, report) with an identity, fingerprint, provenance, and references to producing step.
- **Checkpoint**: The last committed consistent state from which a run can resume.
- **Event**: An immutable, ordered record of something that happened, with actor, time, and cause.
- **Decision Record**: A record of a non-trivial choice and its rationale.
- **Role**: A declared set of responsibilities, instructions, and permitted actions; built-in or custom.
- **Agent Instance**: A running participant bound to a role, a session, and a model choice.
- **Session**: The context thread of exactly one agent instance inside a run, with its own short-term memory and its own resume point; sessions are isolated from each other.
- **Task Board Item**: A unit of work with owner, state, priority, dependencies, and evidence.
- **Message**: An addressed communication between agents with ordering and delivery guarantees.
- **Memory Entry**: A durable or session-scoped piece of knowledge with scope, source, and supersession state.
- **Prompt Template Version**: A versioned, auditable prompt definition with variables, sections, and constraints.
- **Tool**: A registered capability with an input contract, permission scope, timeout, and side-effect declaration.
- **Workspace**: An isolated working area assigned to one agent's work. A git worktree is the standard git-backed kind of workspace; the term "workspace" is the general concept.
- **Budget**: A bounded allowance of attempt count, wall-clock time, or token consumption, declared at run or phase level.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Across a fault-injection suite of 100 interrupted runs covering single-agent, parallel, join, and artifact-write points, at least 95% resume on a fresh instance and produce outcomes equivalent to an uninterrupted control run.
- **SC-002**: In 100% of resume trials, zero already-completed steps are re-executed.
- **SC-003**: In 100% of resume trials that include recorded side effects, zero side effects are duplicated.
- **SC-004**: A fresh reader given only persisted artifacts correctly states the current phase, completed branches, pending work, and next action in at least 90% of 20 sampled scenarios.
- **SC-005**: 100% of phase transitions and recorded decisions are traceable to a record naming actor, time, and cause, verified by an automated audit completeness check.
- **SC-006**: A fan-out of 8 equally sized branches completes in no more than 1.5× the wall-clock time of a single branch on the same machine.
- **SC-007**: *(follow-on increment — memory)* On a curated retrieval set, at least 90% of queries return a relevant entry within the top 5 results.
- **SC-008**: *(follow-on increment — memory)* Context compression reduces prompt size by at least 50% on oversized contexts while retaining 100% of pinned constraints across a regression set.
- **SC-009**: *(follow-on increment — memory)* Semantic cache returns a correct equivalent answer for at least 90% of paraphrased repeats and eliminates at least 30% of recomputation on a repeated-request workload.
- **SC-010**: *(follow-on increment — collaboration)* In 1,000 concurrent claim trials on a shared task board, zero tasks are executed by two agents.
- **SC-011**: 100% of documented core operations are available on every access surface delivered by this increment (library and command line), verified by a parity check.
- **SC-012**: 100% of a 30-case invalid-configuration suite is rejected before a run starts, each with a message naming the offending setting.
- **SC-013**: A new developer reaches a first successful multi-phase run within 15 minutes following the published getting-started guide.
- **SC-014**: Secret scanning over the artifacts, prompts, and logs produced by the test corpus reports zero findings.
- **SC-015**: *(follow-on increment — collaboration)* A non-trivial coding task decomposed into at least five board items is completed end-to-end by at least three distinct roles without manual intervention, in a repeatable integration scenario.
- **SC-016**: Every public contract change is accompanied by a compatibility note, and extension points added through the public contract require no core modification.

## Clarifications

### Session 2026-11-11

- **Q: How should this specification be scoped?** → **A: Core-first.** P1–P3 (phases with fan-out/join, resumability, audit reconstruction) are the committed increment; P4–P8 are accepted direction and become follow-on feature specifications, not part of this plan's deliverable.
- **Q: Which configuration formats must be supported, given the constitution requires YAML/JSON/TOML and the draft named only YAML/JSON?** → **A: All three.** YAML, JSON, and TOML are supported with a documented precedence order; the constitution stays unamended.
- **Q: Where must concurrent ownership of a run be safe in this increment?** → **A: Single host.** Ownership is a lease enforced between processes on one host that share the store, with stale-owner takeover recorded. Cross-host takeover is deferred.
- **Q: What is a session, given the spec used the word for both an entity and a memory scope?** → **A: One agent instance's context thread.** A run groups one session per agent instance; sessions are isolated, each resumes independently, and shared knowledge flows through the board, messaging, and project-scoped long-term memory.
- **Q: Which vocabulary is canonical where terms collide?** → **A: Draft terms, refined.** Run > Phase > Session > Step Attempt; "branch" means a parallel lane inside a phase only (a git branch is always named as such); "workspace" is the general isolated working area and "git worktree" is one kind of workspace.
- **Q: Which durable storage model does this increment require?** → **A: Hybrid.** A transactional single-host store holds run state, checkpoints, leases, and the event log (state and event commit atomically); artifacts are content-addressed files with recorded fingerprints. Both sides stay behind replaceable interfaces.
- **Q: Which budget units must be enforceable?** → **A: Attempt count, wall-clock time, and token consumption,** at run and phase level. Monetary cost is not a mandatory budget in this increment.
- **Q: How does an agent reach a model?** → **A: Provider-neutral contract plus one reference adapter.** The contract covers prompt input, structured result, token usage, cancellation, and error classification; provider-specific code stays behind it, and tests use deterministic fakes so recoverability tests run offline.
- **Q: Which surfaces must this increment deliver, given story 7 was deferred?** → **A: Library plus a minimal command line** (start, resume, status, list artifacts). HTTP and gRPC remain in the follow-on increment.
- **Q: How is a workflow defined?** → **A: Declarative document plus programmatic builders.** A definition is a reviewable, versionable file in the supported formats; the library builds the same in-memory definition; both validate identically.
- **Q: How much of the role model belongs to this increment?** → **A: Agent identity only.** An agent is a named participant with instructions, a model selection, and budgets; the six built-in roles, permission scoping, task board, and messaging ship with the follow-on collaboration increment.
- **Q: What does "the exact point of interruption" mean for a mid-flight step?** → **A: Attempt-level atomicity.** A step attempt commits wholly or not at all; resume restarts an uncommitted attempt from the phase's recorded inputs, records the restart and its cause, and relies on idempotency keys so recorded side effects are never duplicated.

## Assumptions

- **Durable storage (settled)**: run state, checkpoints, ownership leases, and the event log live in a transactional single-host store; artifacts live as content-addressed files with recorded fingerprints. Both remain replaceable behind their interfaces, and artifacts stay human-readable for audit and export.
- **Deferred to planning**: vector indexing strategy, embedding provider choice, packaging and distribution model, and the concrete default budgets, retries, timeouts, and concurrency.
- **Provider neutrality**: teams supply their own model access; the framework does not mandate a specific model provider and must not persist provider-specific continuation state that a different provider could not use.
- **Scope of this specification**: this document is the product baseline, but **P1–P3 (ordered phases with fan-out/join, resumability, audit reconstruction) are the committed scope of this feature increment**. P4–P8 remain described here as the accepted product direction and are marked as follow-on increments; each becomes its own feature specification before implementation. Requirements and success criteria tied exclusively to P4–P8 are therefore satisfied in later increments, not by this one.
- **Single-project scope for v1**: memory, boards, and runs are scoped to one project/workspace at a time; cross-project sharing is an explicit, opt-in operation rather than the default.
- **Concurrency expectations**: a single run may execute up to tens of branches concurrently; the target is engineering workloads on a single team scale, not internet-scale multi-tenancy.
- **Single-host execution in this increment**: one host owns a run at a time. The lease and takeover mechanism is designed so that cross-host coordination can be added later without changing workflow definitions, but distributed consensus is not part of this increment.
- **Trust model**: operators are trusted; agents, model output, retrieved documents, and tool output are untrusted.
- **Retention**: event logs and artifacts are retained for the life of a run, with archival or deletion at the operator's discretion; secrets are never part of them.
- **Testing expectation**: determinism and offline runnability are required for critical-path tests, so model and tool interactions must be replaceable with fakes.
- **Accessibility of documentation**: audit and export formats are human-readable so that recovery and review do not depend on a running instance.
