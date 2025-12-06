import React from 'react'
import ReactDOM from 'react-dom/client'
import { Toaster } from 'sonner'
import './index.css'
import { RouterProvider } from 'react-router-dom'
import { router } from './routes'
import { LanguageProvider } from './contexts/LanguageContext'
import { AuthProvider } from './contexts/AuthContext'
import { ConfirmDialogProvider } from './components/ConfirmDialog'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <LanguageProvider>
      <AuthProvider>
        <ConfirmDialogProvider>
          <Toaster
            theme="dark"
            richColors
            closeButton
            position="top-center"
            duration={2200}
            toastOptions={{
              className: 'nofx-toast',
              style: {
                background: '#0b0e11',
                border: '1px solid var(--panel-border)',
                color: 'var(--text-primary)',
              },
            }}
          />
          <RouterProvider router={router} />
        </ConfirmDialogProvider>
      </AuthProvider>
    </LanguageProvider>
  </React.StrictMode>
)
