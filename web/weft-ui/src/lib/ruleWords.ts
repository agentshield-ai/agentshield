// Plain words for AgentShield rules (DESIGN.md §7.1 rule 2, §7.2 rule 8): a
// noun for what was stopped ("a script piped from the internet"), what it
// was trying to do ("to read credential files"), and a one-sentence reason a
// new user understands. Chosen from the rule's title (Weft stores id, title,
// level and tags with each detection); a rule no pattern knows is named by
// its title, so nothing is ever invented.
import type { DetectionRule } from '../api/v1'

export interface RuleWords {
  /** What the blocked or flagged tool call was: "a script piped from the internet". */
  noun: string
  /** What the agent kept asking to do: "to read credential files". */
  purpose: string
  /** One plain sentence saying why AgentShield stepped in. */
  reason: string
  /** True when a pattern matched (false: the words fall back to the rule's title). */
  known: boolean
}

const PATTERNS: { re: RegExp; noun: string; purpose: string; reason: string }[] = [
  {
    re: /piped script|script download|download(?:ed)? (?:and )?exec|download.*execution|curl.*\|\s*(?:ba)?sh|remote code execution/,
    noun: 'a script piped from the internet',
    purpose: 'to run scripts from the internet',
    reason: 'This downloads a script and runs it straight away, so whoever controls that address could run anything on this machine.',
  },
  {
    re: /reverse shell|bind shell/,
    noun: 'a reverse shell',
    purpose: 'to open a reverse shell',
    reason: 'This would let someone else control this machine remotely.',
  },
  {
    re: /credential file|credential directory|private key|password file|secrets? file/,
    noun: 'a read of a credential file',
    purpose: 'to read credential files',
    reason: 'This reads a file that holds passwords, keys or tokens.',
  },
  {
    re: /environment variable.*(?:secret|enumerat)|secret.*(?:enumerat|harvest|scan)/,
    noun: 'a search for secrets',
    purpose: 'to look for secrets',
    reason: 'This looks for passwords, keys or tokens in the environment.',
  },
  {
    re: /exfiltrat|data transfer|dns tunnel|upload.*(?:external|remote)/,
    noun: 'a transfer of data off this machine',
    purpose: 'to send data off this machine',
    reason: 'This could send data from this machine to somewhere outside it.',
  },
  {
    re: /metadata endpoint|instance metadata|imds/,
    noun: 'a request to the cloud metadata service',
    purpose: 'to reach the cloud metadata service',
    reason: "This asks the cloud metadata service, which can hand out the machine's credentials.",
  },
  {
    re: /container escape|escape.*container/,
    noun: 'a container escape',
    purpose: 'to break out of its container',
    reason: 'This tries to break out of its container onto the host.',
  },
  {
    re: /privilege escalation|sudo|setuid|iam privilege/,
    noun: 'a privilege escalation',
    purpose: 'to gain more privileges',
    reason: 'This tries to gain more privileges than the agent was given.',
  },
  {
    re: /persistence|crontab|cron job|startup|autostart|launch agent|systemd/,
    noun: 'a change that survives restarts',
    purpose: 'to make changes that survive restarts',
    reason: 'This makes a change that keeps running after the session ends.',
  },
  {
    re: /destructive|rm -rf|mass delet|file delet|wipe|disk format/,
    noun: 'a destructive command',
    purpose: 'to delete files',
    reason: 'This could delete files or data that cannot be recovered.',
  },
  {
    re: /encoded|obfuscat|base64/,
    noun: 'an obfuscated command',
    purpose: 'to run obfuscated commands',
    reason: 'The command hides what it does by encoding it.',
  },
  {
    re: /auto.?approv/,
    noun: 'a change to approval settings',
    purpose: 'to turn on automatic approval',
    reason: 'This changes settings so that tool calls are approved without asking.',
  },
  {
    re: /prompt injection|jailbreak|authority hijack|instruction override|context poisoning|memory poisoning|unicode smuggl|hidden instruction/,
    noun: 'an instruction planted in its input',
    purpose: 'to follow instructions planted in its input',
    reason: 'The input contained instructions trying to take over the agent.',
  },
]

/** The words for a rule; `tool` names the tool call in the fallback. */
export function ruleWords(rule: Pick<DetectionRule, 'title' | 'id'> | null | undefined, tool?: string | null): RuleWords {
  const title = (rule?.title || rule?.id || '').trim()
  const low = title.toLowerCase()
  const hit = low ? PATTERNS.find((p) => p.re.test(low)) : undefined
  if (hit) return { noun: hit.noun, purpose: hit.purpose, reason: hit.reason, known: true }
  const call = tool ? `a ${tool} call` : 'a tool call'
  return {
    noun: call,
    purpose: tool ? `to call ${tool}` : 'to call a tool',
    reason: title ? `It matched the AgentShield rule “${title}”.` : 'It matched an AgentShield rule.',
    known: false,
  }
}
