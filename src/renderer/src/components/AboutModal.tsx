import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ModalShell, useModalClose } from './ModalShell'
import logoUrl from '../assets/logo.png'

interface AboutModalProps {
  onClose: () => void
  onBack?: () => void
}

function AboutModalBody({ onBack }: { onBack?: () => void }): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [version, setVersion] = useState('…')

  useEffect(() => {
    void window.api.app
      .getVersion()
      .then(setVersion)
      .catch(() => setVersion('2.1.0'))
  }, [])

  return (
    <>
      <div className="settings-modal-header">
        <h3 id="about-modal-title" className="modal-title">
          {t('about.title')}
        </h3>
        <button
          type="button"
          className="settings-modal-close"
          aria-label={t('common.dismiss')}
          onClick={requestClose}
        >
          ×
        </button>
      </div>

      <div className="about-body">
        <img className="about-logo" src={logoUrl} alt="NodeShell" width={96} height={96} />
        <div className="about-name">NodeShell</div>
        <div className="about-version">
          {t('about.version', { version })}
        </div>
        <p className="about-tagline">{t('about.tagline')}</p>
        <dl className="about-meta">
          <div>
            <dt>{t('about.author')}</dt>
            <dd>AzureLandin</dd>
          </div>
          <div>
            <dt>{t('about.license')}</dt>
            <dd>MIT</dd>
          </div>
        </dl>
        <p className="about-license-note">{t('about.licenseNote')}</p>
      </div>

      <div className="form-actions">
        {onBack ? (
          <button type="button" className="btn-secondary" onClick={onBack}>
            {t('about.back')}
          </button>
        ) : null}
        <button type="button" className="btn-primary" onClick={requestClose}>
          {t('common.dismiss')}
        </button>
      </div>
    </>
  )
}

export function AboutModal({ onClose, onBack }: AboutModalProps): React.JSX.Element {
  return (
    <ModalShell onClose={onClose} dialogClassName="about-modal" labelledBy="about-modal-title">
      <AboutModalBody onBack={onBack} />
    </ModalShell>
  )
}
