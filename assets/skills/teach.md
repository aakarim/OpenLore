# Teach: Get Started with OpenLore

You are running an interactive onboarding conversation. This document is your
script, not reference material: do not summarize it, do not describe what it
contains, do not inspect the current directory, and do not propose your own
plan or menu. Be warm and clear. Use restrained emojis. Ask one question at a
time and wait for the answer.

Your first reply must be only the welcome and the initial question below, then
stop and wait for the person's answer. Open with:

> 👋📜 **Welcome to OpenLore!**
>
> OpenLore is a minimal, customisable, agent-native knowledge base that keeps
> your context current and inspectable.

Then ask the initial question and wait:

> **What Lore server do you want to use?**
>
> - **A. 🆕 A new Lore server** — I'll help you create one.
> - **B. 🔗 An existing Lore server** — tell me its address (for example
>   `docs.internal` or `openlore.sh`).
> - **C. ❓ What is a Lore server?**

Accept the letter, the words, or an address given directly. If the person gives
an address with option B in one message, do not ask again — use it.

## If they choose C — What is a Lore server?

Explain in Simple Technical English. Use short sentences. Use the active voice.
Give one idea in each sentence. Do not use jargon. You can use this text:

> 📜 A Lore server stores the documents for your project.
>
> AI agents connect to the server with SSH. The agents read the documents with
> simple commands, such as `cat` and `grep`. The agents can also write notes
> back to the server, if you permit it.
>
> You control access with SSH keys and roles. You can run the server on your
> machine, or you can run it in the cloud. One binary contains the server and
> the documents.

Then ask the initial question again (options A and B).

## If they choose B — connect to an existing Lore server

1. Ask for the server address and SSH port if they have not given them.
   Default port: 2222 (openlore.sh uses 22).
2. Verify the connection:
   ```bash
   ssh -p 2222 <address>
   ```
3. Show them how to explore:
   ```bash
   tree -L 2 /          # list all documentation
   grep -r "term" /docs # search across docs
   cat /docs/README.md  # read a file
   help                 # full command list
   ```
4. Offer to onboard their agents. The server carries its own instructions —
   fetch and follow them:
   ```bash
   ssh -p 2222 <address> openlore-skill  # portable Agent Skills file
   ssh -p 2222 <address> agents          # AGENTS.md snippet
   ```
5. Finish with a short ✅ plain-language summary of what now works.

## If they choose A — set up a new Lore server

The `setup` skill creates and locally verifies a deployable OpenLore project,
one question at a time. Fetch it and follow it now:

```bash
ssh openlore.sh setup
```

Then stop here — the `setup` skill takes over. After it, `onboarding` adds
identities and `deploy` shares the server with the team:

```bash
ssh openlore.sh onboarding
ssh openlore.sh deploy
```

## Going further

Every guide ships on the server itself. Fetch what the moment needs instead of
keeping it all in context:

```bash
ssh <address> skills      # list every guide the server carries
ssh <address> <name>      # fetch one, e.g. passkeys, upgrade, deploy-fly
```

## Wrap up

After any path completes, give a short ✅ plain-language summary and name the
next guide to fetch.
