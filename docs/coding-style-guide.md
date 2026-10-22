# Share a coding style guide with every agent

Keep one coding style guide on the server, have an agent write it from a published guide such as Google's, point every agent at it from `AGENTS.md`, and cap its size so reading it costs a known amount of context.

In this recipe you can:

- Put the style guide in a `style` docset that a curator edits and every agent reads, so one update reaches every repository and every agent on its next read.
- Hand a coding agent one prompt that reads a published style guide, adapts it to your team, and writes it into OpenLore as a short guide plus one page per decision.
- Tell agents where the guide is with a few lines in `AGENTS.md` or `CLAUDE.md`, and tell them to read only the page they need.
- Set a line budget on the guide and a token budget on every page, so the server rejects a write that would make the guide too big to read at the start of every session.

The end result is a `/style` docset with a `go/guide.md` of at most 300 lines that an agent reads in full, a `go/decisions/` folder of pages under 2,000 tokens each that an agent reads one at a time, and an `index.md` that says what lives where. The examples use Go and the [Google Go Style Guide](https://google.github.io/styleguide/go/); swap in the Python, Java or C++ guide from the same [styleguide](https://google.github.io/styleguide/) site for another language.

---

## Why share a style guide through OpenLore

A style guide in a repository reaches the agents that work in that repository. The same guide pasted into five repositories drifts into five guides, and an agent working across two of them follows whichever it read last. One copy on the server, read by path, is the same text in every session.

**Agents follow what they read, not what they were told once.** A rule in a code review comment governs one pull request. A rule in a file the agent reads at the start of each session governs every pull request. The guide is where "we return errors, we do not panic" and "tests use `t.Run` with a table" stop being review feedback and start being the default.

**The guide and its reasoning have different readers.** Google splits its Go style into a short [Style Guide](https://google.github.io/styleguide/go/guide) everyone reads, a long [Style Decisions](https://google.github.io/styleguide/go/decisions) document for reviewers, and [Best Practices](https://google.github.io/styleguide/go/best-practices) for anyone interested. The split matters more for an agent than for a person: the guide is 483 lines and about 4,900 tokens; the decisions page is 3,883 lines and about 31,500 tokens. An agent that reads both on every session spends almost a fifth of a 200k context window before it opens a source file. Keeping the guide short and the decisions one page per topic lets an agent read the rules every time and the reasoning only when a question comes up.

**The size limit is enforced, not requested.** A line that says "keep this file short" in a prompt lasts until the first agent appends a section. A `size/lines` rule on the guide and a `size/tokens` rule on every page are checked by the server on every write ([Folder rules](folder-rules.md)). A write that would push the guide over budget is rejected with a message that tells the agent to move the new material into a linked page instead.

**Who changed a rule is on the record.** Every write is journaled with its author, including `person/agent` when an agent acted on someone's behalf ([`history`](commands.md#history---principal-name---actor-name)). The dashboard shows how often each page is read and what it costs, so you can see which decisions agents actually look up and which nobody opens.

---

## Before you begin

- A running server with writes enabled (`readonly: false`) and `auth_file` pointing at a `lore.json`. [Any agent over SSH](start-ssh.md) and [Configure the server](configure-the-server.md) cover both.
- An SSH key for yourself as curator, and a coding agent connected to the server: Claude Code over MCP ([Claude Code with OpenLore](start-claude-code.md)) or any agent with an SSH identity ([Any agent over SSH](start-ssh.md)).
- A repository the agent can read while it writes the guide, so the team-specific rules come from your code and tooling rather than from the agent's memory.
- Understand what a [folder rule](folder-rules.md) is and how a [rejected write](writing.md#write--validate--finish) looks from the agent's side.

---

## 1. Create the style docset with its budgets

One docset holds every language's guide. The `curator` role writes it; everyone else reads it. The two rules are the budgets: `guide-length` caps any `guide.md` at 300 lines, and `page-size` caps every Markdown file at 2,000 estimated tokens, except the guides, which the line cap already governs.

```json
{
  "roles": {
    "curator": {},
    "staff": {}
  },
  "docsets": {
    "style": {
      "paths": ["/style"],
      "access": { "allow": { "curator": "rw", "staff": "ro" } },
      "config": { "edit": ["curator"] },
      "rules": {
        "guide-length": { "match": ["*/guide.md"], "use": "size/lines", "with": { "max": 300 } },
        "page-size": { "match": ["**/*.md"], "exclude": ["*/guide.md"], "use": "size/tokens", "with": { "max": 2000 } }
      }
    }
  },
  "identities": [
    { "name": "adil", "public_key": "ssh-ed25519 AAAA…", "roles": ["curator", "staff"] },
    { "name": "coding-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["staff"] }
  ]
}
```

Restart the server and check the docset and the rules are in effect.

```bash
lore docsets
lore package doc size/tokens
```

The first command lists `style` with `rw` for the curator. The second prints the member's parameters and confirms it is evaluated on every write.

The numbers are a starting point. A 300-line guide is roughly 3,000 to 4,000 tokens, which an agent can read at the start of every session without noticing. `size/tokens` uses the built-in estimator `estimate/v1`, `ceil(bytes / 4)`, so leave a margin rather than tuning to a provider's tokenizer.

> **Tip:** Give the agent that writes the guide the `curator` role for the session in which it writes it, then take the role away. Day to day, agents only need `staff`. If you would rather never grant an agent `rw` on `style`, give it `publish` instead and move its drafts out of the inbox yourself ([Let an agent publish into an inbox](publish-to-inbox.md)).

---

## 2. Have an agent write the guide

Give a coding agent the prompt below from inside the repository the guide should describe. The agent fetches Google's guide with its own web tool, because the restricted shell on the server has no network access, and writes the result into `/style` through the MCP `lore_bash` tool or `ssh`. Replace `go` with your language and the Google URLs with the matching guide.

```text
Write our team's Go style guide into the OpenLore knowledge repository. Use
the openlore MCP shell for every command on the server (or
`ssh -p 2222 <server> '<command>'`); the server shell has no network, so
fetch web pages with your own tools.

1. Read the Google Go Style Guide at
   https://google.github.io/styleguide/go/guide and skim the Style Decisions
   at https://google.github.io/styleguide/go/decisions. Keep the guide's
   structure: style principles first, then core guidelines.

2. Read this repository's go.mod, linter configuration, Makefile, any
   existing AGENTS.md or CONTRIBUTING.md, and a sample of the packages under
   internal/ and pkg/. Note where our code already differs from Google's
   guidance (for example error wrapping, logging, test layout, package
   naming) and treat the existing convention as ours unless it is
   contradicted by a linter we run.

3. Write /style/go/guide.md with tee. It must be under 300 lines; the
   server rejects a longer file. It is the file every agent reads in full
   before writing Go here, so every line must be a rule or a principle,
   with at most one short example per rule. Drop anything specific to
   Google's internal process or libraries. Add the rules from step 2 under
   their own heading, "Local conventions". Where a rule has reasoning
   longer than a sentence, state the rule and link to a decisions page:
   `See [decisions/errors.md](decisions/errors.md)`.

4. Write one page per topic under /style/go/decisions/, following the
   sections of Google's Style Decisions (naming, commentary, imports,
   errors, language, testing, and whatever local topics step 2 produced).
   Each page must stay under 2,000 tokens, which is about 8,000 bytes; the
   server rejects a larger page. Open each page with the rule in one
   sentence, then the reasoning and the examples. Cite the Google section
   the page is adapted from.

5. Write /style/index.md: one paragraph on how to use the docset (read
   guide.md in full, read one decisions page when a question comes up),
   then a list of every file with a one-line description.

6. Check the budgets before you finish:
   `wc -l /style/go/guide.md` and
   `wc -c /style/go/decisions/*.md`. Then run `lore validate /style`. If a
   write is rejected by a size rule, follow its `suggested:` line and move
   material into a linked page; do not retry with small trims.

7. Reply with the path of every file you wrote, the line count of
   guide.md, and the byte count of the largest decisions page.

Write only inside /style/go and /style/index.md.
```

The agent writes the guide, the decisions pages and the index, and reports their sizes. Read `guide.md` yourself before telling anyone about it: it is the file that will be in every agent's context, and a wrong rule in it is a wrong rule everywhere.

> **Tip:** Run `history | jq -c 'select(.target | startswith("/style")) | {time, attribution, target}'` afterwards. Every page is attributed to the agent, or to `you/agent` if it wrote through an OAuth client, so the journal already says where the guide came from.

---

## 3. Point agents at the guide from `AGENTS.md`

The server's `agents` command prints a section that tells an agent how to connect and search ([Any agent over SSH](start-ssh.md#4-teach-the-agent-the-conventions)). Add a short section after it that names the guide and says how much of the docset to read. Put it in `AGENTS.md`, or `CLAUDE.md` for Claude Code, in every repository the guide covers.

```markdown
## Coding style

The team's Go style guide lives in the OpenLore knowledge repository.
Before writing or reviewing Go, read the guide in full:

    ssh -p 2222 <server> cat /style/go/guide.md

It is under 300 lines, so always read all of it. The reasoning behind each
rule is in /style/go/decisions/, one page per topic. When a rule needs
explaining or the guide links to a page, read that page and only that page;
never cat the whole folder. If the guide and the code you are editing
disagree, follow the guide and say so in your reply.
```

For an agent on MCP, change the command line to `cat /style/go/guide.md` and say it runs in the openlore `lore_bash` tool. Everything else is the same: the path is the same on every transport, and the identity's grant decides what it can see.

The section costs an agent one `cat` per session for the guide and one more for a decisions page when a question comes up. With the budgets from step 1 that is at most 300 lines plus 2,000 tokens, a number you chose rather than one that grows with the guide.

> **Caution:** `cat /style/go/decisions/*.md` reads every page in one command. The instruction above says never to do that, and the `size/tokens` cap bounds the damage if an agent does it anyway, but a `staff` grant cannot stop it. Keep the number of decisions pages in view: twenty pages at 2,000 tokens each is still 40,000 tokens if read at once.

---

## 4. Keep the guide within budget

The rules from step 1 run on every write to `/style`, from any agent or person and over any transport. An agent that appends a section to a full guide sees the rejection below and the write is not saved.

```text
rules: /style/go/guide.md: size/lines (guide-length @ lore.json#docsets.style)
  318 lines exceeds the limit of 300 (max: 300)
  this file cannot grow past 300 lines under this rule
  suggested: keep guide.md under 300 lines; move the new material into a sibling file such as guide-details.md and add a link to it from guide.md so readers can drill in
  override: to raise the limit, edit guide-length in lore.json#docsets.style
  see: lore package doc size/lines
```

The `suggested:` line is the right remedy for a style guide: the rule stays in `guide.md` as one sentence and the reasoning goes into a decisions page. A `size/tokens` rejection on a decisions page reads the same way with `tokens` as the unit, and the answer is the same: split the topic into two pages, each under the cap.

To see what the guide costs before an agent reads it, measure it in the shell. Tokens are bytes divided by four under `estimate/v1`.

```bash
wc -l -c /style/go/guide.md
wc -c /style/go/decisions/*.md | grep -v total | sort -rn | head -5
```

The first line is the guide's lines and bytes; the second list is the five largest pages. The dashboard's Analytics tab shows the same estimated context cost per file alongside how often each is read, so a decisions page nobody opens in a month is a candidate to fold into another or drop ([Prune a knowledge repository from its analytics](knowledge-cleanup.md)).

Once the decisions pages have been reviewed, you can freeze them near their current size instead of at a fixed number. A `.lore/config.yaml` in the decisions folder adds a growth limit under a new name; a page may then grow by 10% from the size it had when the rule was added, and a curator can [reset the baseline](folder-rules.md#inspecting-and-resetting-a-baseline) when a rewrite is deliberate.

```yaml
version: 1
rules:
  decision-frozen:
    match: ["*.md"]
    use: size/lines
    with: { max: initial, growth: 1.1 }
```

Write it as the curator with `tee /style/go/decisions/.lore/config.yaml`; only roles in the docset's `config.edit` can.

---

## Verify

Connect as an agent would and read the guide the way `AGENTS.md` says to.

```bash
ssh -p 2222 -i ~/.ssh/coding-agent <server> "wc -l /style/go/guide.md; ls /style/go/decisions; lore validate /style"
```

You should see a line count under 300, one file per topic, and `0 errors, 0 warnings` from `lore validate`, which exits 0 when every page is within budget.

```text
 284 /style/go/guide.md
commentary.md
errors.md
imports.md
local-conventions.md
naming.md
testing.md
0 errors, 0 warnings
```

Then start a session in a repository with the `AGENTS.md` section from step 3 and ask the agent to add a small function. Its first command should be the `cat` of the guide, and the code it writes should follow a rule from "Local conventions" that Google's guide does not have.

---

## Next steps

- Add a second language as `/style/python/guide.md` and `/style/python/decisions/`, written from the [Google Python Style Guide](https://google.github.io/styleguide/pyguide.html) with the same prompt; the `*/guide.md` and `**/*.md` globs from step 1 already cover it.
- [Save the writing prompt as a skill](knowledge-cleanup.md#7-save-the-prompt-as-a-skill) in a `skills` docset, so an agent can refresh the guide against a newer Google revision on request.
- [Let agents propose rule changes through an inbox](publish-to-inbox.md): a `publish` grant on `style` lets an agent file a suggested decisions page without being able to edit the guide.
