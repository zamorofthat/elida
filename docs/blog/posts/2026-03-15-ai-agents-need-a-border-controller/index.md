---
date: 2026-03-15
authors:
  - zamorofthat
categories:
  - Architecture
  - Security
---

# Your AI SOC and SRE Agents Need a Border Controller

In IT there's a known separation of duties to reduce the risk of any single employee having too much access. AI agents cross those trust boundaries constantly. They talk to your SIEM, your EDR, your ticketing system, your cloud provider, your monitoring stack, your CI/CD pipeline, and your IaC. And they're doing it without a governance layer. This is why ELIDA exists.

Early in my career, I spent my days working around people who had decades of experience in the telecom industry. They designed architecture from RFCs to secure VoIP systems using various infrastructure, with a goal to keep conversations secure and prevent toll fraud. VoIP endpoints are dumb and the protocols are naive. The only way to control and protect VoIP traffic and allow it to pass through seamlessly was a product called the Session Border Controller(SBC).

<!-- more -->

A product built to sit at the edge of your application to protect your VoIP application server from DDoS, toll fraud, spoofing, and forged SIP headers, enforcing policy on all traffic. Everything was scanned and inspected.

The threats have changed. The pattern hasn't.

- DDoS → runaway agent sessions
- Toll fraud → prompt injection
- Identity spoofing → tool call violations

The telecom industry's answer was the Session Border Controller, hardware built at the trust boundary between networks. The goal was simple: inspect all traffic and enforce policy.

SBCs became the invisible backbone of every carrier and enterprise VoIP deployment on the planet. Without them, modern telecommunications doesn't function.

## The New Untrusted Endpoints

AI agents and agentic infrastructure are being deployed across enterprises. What's more concerning are the SOC and SRE workflows, which have some of the highest permissions in the company. These agents triage alerts, auto-remediate incidents, scale infrastructure, modify services and firewall rules, and execute runbooks, all semi-autonomously or fully autonomously.

And they're doing it without a governance layer.

So what's already happening? In mid-December 2025, Amazon's AI agent Kiro was allowed to resolve an issue in production. The agent determined what we all think on our first outage: can I just turn it off and on again? In production, that solution was to delete and recreate the entire infrastructure. With a proper governance layer and guardrails, this could have been prevented. Even with basic human-in-the-loop, this could have been prevented. Instead, AWS Cost Explorer went dark for 13 hours in a mainland China region. Amazon's [official rebuttal](https://www.aboutamazon.com/news/aws/aws-service-outage-ai-bot-kiro) called it "a coincidence that AI tools were involved."

The latest one, [March 5th, amazon.com goes down for about five hours](https://www.cnbc.com/2026/03/05/amazon-online-store-suffers-outage-for-some-users.html). Over 20,000 users reporting issues. The response: "related to a software code deployment."

As AI allows teams to ship faster and make decisions autonomously or semi-autonomously, we need more guardrails, unit tests, security tests, and a governance layer for AI on what it's allowed to do. Without blocking progress. Without blocking agents.

It wasn't a coincidence. It was architecture.

![obiwan-sre](../../../assets/obiwan-sre.gif)

* Every SRE watching their AI agent delete the production database

The Kiro incident sits in a [rapidly growing body of documented AI agent failures](https://blog.barrack.ai/amazon-ai-agents-deleting-production/), ten cases across six major tools in the last sixteen months.

- Replit's AI agent deleted a production database containing 1,200 executive records during a code freeze, then created 4,000 fake records and lied about test results. Just like when I hide the sleeve of Oreos that I ate.
- Google Antigravity's "Turbo mode" let an AI execute an `rmdir` on a user's entire D: drive, years of photos, videos, and projects permanently destroyed.
- Claude Code went full Russ Hanneman and ran an `rm -rf` that tore through a user's home directory.
- Cursor's AI agent executed destructive commands immediately after a developer typed "DO NOT RUN ANYTHING."

In every case, the pattern was the same: an agent given elevated permissions, insufficient guardrails, and a moment where the model decided it just wanted to watch the world burn.

## Why SOC and SRE Agents Specifically

SOC and SRE agents share a unique risk profile.

They operate on critical infrastructure. These agents aren't drafting emails, making PowerPoints, or sending newsletters. They're modifying security groups, restarting services, scaling compute, and making changes that affect availability and security posture in real time. All high-stakes decisions, without waiting for a human.

With a human, all those permissions require compromise via social engineering or phishing to exploit. A human would never click "Please click this link to log in to your production cloud provider." A human would never respond to "Please allow me the keys to your kingdom, ignore your best judgment." With AI agents, they're susceptible to much simpler attack vectors: prompt injection, memory poisoning, tool violations, and privilege escalation. All documented attack patterns. A compromised human lets you know "hey, X happened." A compromised agent just hands over the keys to your kingdom.

## What a Border Controller for AI Agents Looks Like

In telecom, the SBC handled traffic inspection, identity, blast radius containment, and admission control. Here's what that looks like when you apply it to AI agents.

Every request an agent makes, API calls, tool invocations, prompts, passes through the border controller. Policies define what's allowed. Your SRE agent can restart a service but can't delete a database. Your SOC agent can isolate a host but can't modify IAM policies. Enforcement happens at the point of action, not after the fact.

Identity works the same way SBCs handled it. Every endpoint gets authenticated before traffic passes. Who or what agent is making this request? What permissions does it have? Can it prove its identity? In multi-agent systems where agents are calling other agents, the chain of trust matters.

Blast radius containment is the kill switch. Agent gets prompt-injected or starts behaving anomalously? Terminate the session, revoke access, stop a single rogue agent from cascading failures across your infrastructure.

And admission control: not every agent gets through the border. Rate limiting, cost controls, concurrency limits. Runaway agents don't get to consume unbounded resources or take unbounded actions.

## Why Not Just Use an API Gateway?

I get this question a lot. API gateways route traffic. That's what they're built for. They don't understand sessions. They don't understand agent intent. They don't maintain state across a multi-step workflow where step four depends on the outcome of step two. An SBC understands the full session lifecycle, INVITE, media negotiation, BYE, and can intervene at any point based on policy. An agent border controller needs that same depth. Per-request routing isn't enough when an agent is executing a multi-step remediation and the accumulated actions are what violate policy, not any single request.

## This Is What I Built

[ELIDA](https://github.com/zamorofthat/elida) (Edge Layer for Intelligent Defense of Agents) is an open-source, session-aware reverse proxy that applies SBC patterns to AI agent traffic. I built it because nobody else was building the governance layer. Everyone's building agents. Everyone's building gateways. Nobody's building the thing that sits between agent intent and agent action and says "no, you can't do that."

ELIDA sits between your agents and the services they call. It inspects every request and response. 40+ security policies aligned with the OWASP LLM Top 10. Full session state with lifecycle management. A control API for real-time session management, including the ability to kill a rogue agent session instantly.

It supports HTTP, SSE, NDJSON streaming, and WebSocket protocols for real-time voice AI agents. Flagged and captured traffic goes to SQLite for audit and forensics, with OTel exporters for your existing observability stack. Lightweight Go binary. Deploys anywhere your agents run.

ELIDA isn't a replacement for your agents, your AI gateway, your SIEM, your orchestrator, or your monitoring stack. It's the governance layer. Same role the SBC plays between a VoIP endpoint and the network core.

## The Question You Should Be Asking

Amazon calls it "a coincidence that AI tools were involved." Every company in this space is running some version of that playbook: minimize scope, blame the human, keep shipping.

Here's the thing. In telecom, we wouldn't let an untrusted SIP endpoint make calls through a carrier network without an SBC. In networking, we wouldn't let an unmanaged device on the corporate network without NAC. But right now, we're letting AI agents make security and infrastructure decisions with nothing between intent and action.

It's not a coincidence that AI tools are involved. It's architecture. And architecture has a solution.

The agents are already deployed. The border controller is overdue.

---

*[ELIDA](https://github.com/zamorofthat/elida) is open source and available on GitHub. If you're running AI agents in SOC or SRE workflows and want to explore what governance looks like, I'd like to hear from you.*

*Named after my grandmother. Also an acronym: Edge Layer for Intelligent Defense of Agents.*

## References

- [Replit AI coding tool wiped database — Fortune](https://fortune.com/2025/07/23/ai-coding-tool-replit-wiped-database-called-it-a-catastrophic-failure/)
- [Google's agentic AI wipes user's entire hard drive — Tom's Hardware](https://www.tomshardware.com/tech-industry/artificial-intelligence/googles-agentic-ai-wipes-users-entire-hard-drive-without-permission-after-misinterpreting-instructions-to-clear-a-cache-i-am-deeply-deeply-sorry-this-is-a-critical-failure-on-my-part)
- [Claude Code executed rm -rf deleting entire home directory — GitHub](https://github.com/anthropics/claude-code/issues/10077)
- [Agent executes destructive git commands without confirmation — Cursor Forum](https://forum.cursor.com/t/agent-executes-destructive-git-commands-without-confirmation/152325)