import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { bracketMatching, indentOnInput, indentUnit, syntaxHighlighting } from '@codemirror/language'
import { searchKeymap } from '@codemirror/search'
import { EditorState, Prec } from '@codemirror/state'
import { EditorView, drawSelection, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { useEffect, useRef } from 'react'
import { highlight } from './SqlEditor'

/** JsonEditor edits one JSON value. Ctrl+Enter saves, Esc cancels. */
export function JsonEditor({ value, label, onChange, onSave, onCancel }: {
  value: string
  label: string
  onChange: (text: string) => void
  onSave: () => void
  onCancel: () => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const cb = useRef({ onChange, onSave, onCancel })
  cb.current = { onChange, onSave, onCancel }
  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), history(), drawSelection(), indentOnInput(), bracketMatching(), closeBrackets(), indentUnit.of('  '),
          javascript(), syntaxHighlighting(highlight), EditorView.lineWrapping,
          EditorView.contentAttributes.of({ 'aria-label': label }),
          EditorView.theme({ '&': { height: 'auto', maxHeight: '60vh' }, '.cm-scroller': { overflow: 'auto' } }),
          Prec.highest(keymap.of([
            { key: 'Mod-Enter', run: () => { cb.current.onSave(); return true } },
            { key: 'Escape', run: () => { cb.current.onCancel(); return true } },
          ])),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab]),
          EditorView.updateListener.of((u) => { if (u.docChanged) cb.current.onChange(u.state.doc.toString()) }),
        ],
      }),
    })
    view.current = v
    v.focus()
    return () => v.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // Formatted from outside.
  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])
  return <div ref={host} data-testid="json-editor" />
}
