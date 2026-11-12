import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
  fallback?: ReactNode
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('UI crashed:', error, info.componentStack)
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        this.props.fallback ?? (
          <div className="error-boundary">
            <p>界面出错：{this.state.error.message}</p>
            <button
              type="button"
              className="btn-secondary btn-sm"
              onClick={() => this.setState({ error: null })}
            >
              重试
            </button>
          </div>
        )
      )
    }
    return this.props.children
  }
}
