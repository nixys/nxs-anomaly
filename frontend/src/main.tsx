import React from 'react';
import ReactDOM from 'react-dom/client';
import { MantineProvider, createTheme } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import '@mantine/core/styles.css';
import '@mantine/dates/styles.css';
import '@mantine/notifications/styles.css';
// Product motion: the two keyframes used by the lists, and the reduced-motion
// switch that turns all of it off. Loaded after Mantine so it can override.
import './ui/motion.css';
import { App } from './App';
import { AuthProvider } from './auth/AuthProvider';
import { I18nProvider } from './i18n/I18nProvider';

const theme = createTheme({
  primaryColor: 'blue',
  defaultRadius: 'sm',
  fontFamilyMonospace:
    'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',
});

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { refetchOnWindowFocus: false, retry: 1, staleTime: 5_000 },
  },
});

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="dark">
      {/* Bottom right, not top right: while somebody works a table the top right
          corner is the far edge of their attention, and a toast confirming an
          action nobody saw is the same as no confirmation. */}
      <Notifications position="bottom-right" />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          {/* I18nProvider sits inside AuthProvider because the saved language
              and timezone travel on the identity: it needs to know who this is
              before it can honour their choice. */}
          <AuthProvider>
            <I18nProvider>
              <App />
            </I18nProvider>
          </AuthProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </React.StrictMode>,
);
