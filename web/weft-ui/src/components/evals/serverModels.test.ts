import { describe, expect, it } from 'vitest'
import { leaderboardColumns, metricColumn } from './leaderboardUtils'
import { predictUrlError, promptColumns, serverModelKind, serverModelSummary, serverModelWords } from './modelUtils'

describe('server-runnable models', () => {
  it('classifies models', () => {
    expect(serverModelKind({ value: { _class_name: 'LLMStructuredCompletionModel', llm_model_id: 'gpt-4o' } })).toBe('llm')
    expect(serverModelKind({ value: { _class_name: 'RemoteModel', _bases: ['Model'], predict_url: 'https://x/predict' } })).toBe('remote')
    expect(serverModelKind({ value: { _class_name: 'RuntimeModel', runtime: 'vllm' } })).toBe('runtime')
    expect(serverModelKind({ value: { _class_name: 'Model', predict_url: 'ftp://x' } })).toBeNull()
    expect(serverModelKind({ value: { _class_name: 'Model' } })).toBeNull()
    expect(serverModelSummary({ value: { runtime: 'weft:///e/p/object/vllm:abc', runtime_id: 'llama' } })).toBe('runtime vllm:abc / llama')
    expect(serverModelSummary({ value: { predict_url: 'https://x/p' } })).toBe('POST https://x/p')
  })
  it.each([
    [{ _class_name: 'LLMStructuredCompletionModel', llm_model_id: 'gpt-4o' }, 'calls gpt-4o'],
    [{ _class_name: 'LLMStructuredCompletionModel', llm_model_id: 'mock/echo' }, 'an offline test model that repeats the prompt'],
    [{ predict_url: 'https://x/p' }, 'calls your endpoint at https://x/p'],
    [{ runtime: 'weft:///e/p/object/vllm:abc', runtime_id: 'llama' }, 'runs on the vllm runtime'],
    [{ _class_name: 'Model' }, 'cannot run on the server'],
  ])('serverModelWords %#', (value, out) => expect(serverModelWords({ value })).toBe(out))
  it('validates predict URLs and finds template columns', () => {
    expect(predictUrlError('https://x/predict')).toBeNull()
    expect(predictUrlError('')).toMatch(/Enter/)
    expect(predictUrlError('file:///etc/passwd')).toMatch(/http/)
    expect(predictUrlError('not a url')).toMatch(/valid/)
    expect(promptColumns('Q: {question} ctx {doc.text} {question} {{x}}')).toEqual(['question', 'doc.text', 'x'])
  })
})

describe('leaderboards from comparisons', () => {
  it('splits metric keys into scorer and summary path', () => {
    expect(metricColumn({ key: 'exact.true_fraction', scorer: 'exact', path: 'true_fraction' })).toEqual({ scorer_name: 'exact', summary_metric_path: 'true_fraction' })
    expect(metricColumn({ key: 'model_latency.mean' })).toEqual({ scorer_name: 'model_latency', summary_metric_path: 'mean' })
  })
  it('makes one column per weighted metric and evaluation', () => {
    const cols = leaderboardColumns(
      [{ evaluation_ref: 'e1' }, { evaluation_ref: 'e1' }, { evaluation_ref: 'e2' }],
      [
        { key: 'exact.true_fraction', weight: 2, lowerBetter: false },
        { key: 'model_latency.mean', weight: 1, lowerBetter: true },
        { key: 'ignored.mean', weight: 0, lowerBetter: false },
      ],
      [{ key: 'exact.true_fraction', scorer: 'exact', path: 'true_fraction' }],
    )
    expect(cols).toEqual([
      { evaluation_object_ref: 'e1', scorer_name: 'exact', summary_metric_path: 'true_fraction', should_minimize: false },
      { evaluation_object_ref: 'e1', scorer_name: 'model_latency', summary_metric_path: 'mean', should_minimize: true },
      { evaluation_object_ref: 'e2', scorer_name: 'exact', summary_metric_path: 'true_fraction', should_minimize: false },
      { evaluation_object_ref: 'e2', scorer_name: 'model_latency', summary_metric_path: 'mean', should_minimize: true },
    ])
  })
})
