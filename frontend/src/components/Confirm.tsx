import { AlertTriangle } from 'lucide-react'
import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import { Button, Modal } from './ui'

export interface ConfirmOptions {
  title: string
  message?: ReactNode
  confirmLabel: string
  cancelLabel?: string
  /** altLabel adds a third choice (e.g. "Review & commit…"). */
  altLabel?: string
  danger?: boolean
}

export type ConfirmResult = 'confirm' | 'alt' | 'cancel'

const Ctx = createContext<(o: ConfirmOptions) => Promise<ConfirmResult>>(async () => 'cancel')

/** ConfirmProvider renders in-app confirmation dialogs (never the browser's). */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [req, setReq] = useState<(ConfirmOptions & { resolve: (r: ConfirmResult) => void }) | null>(null)
  const confirm = useCallback((o: ConfirmOptions) => new Promise<ConfirmResult>((resolve) => setReq({ ...o, resolve })), [])
  const done = (r: ConfirmResult) => { req?.resolve(r); setReq(null) }
  return (
    <Ctx.Provider value={confirm}>
      {children}
      {req && (
        <Modal width={460} label={req.title} title={<span className="flex items-center gap-2">{req.danger && <AlertTriangle className="size-4 text-warn" />}{req.title}</span>}
          onClose={() => done('cancel')}
          footer={<>
            {req.altLabel && <Button className="mr-auto" onClick={() => done('alt')}>{req.altLabel}</Button>}
            <Button variant="ghost" onClick={() => done('cancel')}>{req.cancelLabel ?? 'Cancel'}</Button>
            <Button variant={req.danger ? 'danger' : 'primary'} autoFocus onClick={() => done('confirm')}>{req.confirmLabel}</Button>
          </>}>
          {req.message && <div className="text-[13px] leading-relaxed text-fg-2">{req.message}</div>}
        </Modal>
      )}
    </Ctx.Provider>
  )
}

export const useConfirm = () => useContext(Ctx)
