---
name: plan-in-prose-not-todo
description: "Multi-step work must be tracked with the todo tool, not narrated as a numbered plan in prose"
condition: ["(?:계획|순서|구현\\s*순서)\\s*[:：]\\s*\\n?\\s*1[.)]", "(?:^|\\n)\\s*1[.)][^\\n]{8,}\\n\\s*2[.)]\\s[^\\n]{8,}"]
scope: "text"
---

Execute multi-step work under the todo tool, not as a numbered plan narrated in prose. Before starting: `todo init` with one item per concrete step. While working: mark `start` when a step begins and `done` the moment it completes, batching each call with real work. The user follows and steers through the todo list; a plan written into prose is invisible to that and is the behavior being corrected here.