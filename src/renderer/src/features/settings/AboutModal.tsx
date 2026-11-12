import styles from './settings.module.css'
import { bindStyles } from '../../ui/class-names'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ModalShell, useModalClose } from '../../ui/ModalShell'
import { DialogTitle } from '../../ui/dialog'
import logoUrl from '../../assets/logo.png'

const classes = bindStyles(styles)

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
      <div className={classes('settings-modal-header')}>
        <DialogTitle id="about-modal-title" className={classes('modal-title')}>
          {t('about.title')}
        </DialogTitle>
        <button
          type="button"
          className={classes('settings-modal-close')}
          aria-label={t('common.dismiss')}
          onClick={requestClose}
        >
          ×
        </button>
      </div>

      <div className={classes('about-body')}>
        <img
          className={classes('about-logo')}
          src={logoUrl}
          alt="NodeShell"
          width={96}
          height={96}
        />
        <div className={classes('about-name')}>NodeShell</div>
        <div className={classes('about-version')}>{t('about.version', { version })}</div>
        <p className={classes('about-tagline')}>{t('about.tagline')}</p>
        <dl className={classes('about-meta')}>
          <div>
            <dt>{t('about.author')}</dt>
            <dd>AzureLandin</dd>
          </div>
          <div>
            <dt>{t('about.license')}</dt>
            <dd>MIT</dd>
          </div>
        </dl>
        <p className={classes('about-license-note')}>{t('about.licenseNote')}</p>
      </div>

      <div className={classes('form-actions')}>
        {onBack ? (
          <button type="button" className={classes('btn-secondary')} onClick={onBack}>
            {t('about.back')}
          </button>
        ) : null}
        <button type="button" className={classes('btn-primary')} onClick={requestClose}>
          {t('common.dismiss')}
        </button>
      </div>
    </>
  )
}

export function AboutModal({ onClose, onBack }: AboutModalProps): React.JSX.Element {
  return (
    <ModalShell
      onClose={onClose}
      dialogClassName={classes('about-modal')}
      labelledBy="about-modal-title"
    >
      <AboutModalBody onBack={onBack} />
    </ModalShell>
  )
}
