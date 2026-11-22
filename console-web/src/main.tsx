import { MantineProvider } from '@mantine/core';
import '@fontsource-variable/plus-jakarta-sans';
import '@fontsource-variable/jetbrains-mono';
import '@mantine/core/styles.css';
// Generated rules that Mantine would otherwise inject as <style> elements; see lib/mantine-vars.ts.
import './mantine-vars.css';
import '@authgate/web-kit/styles.css';
import './styles.css';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { App } from './App';
import { theme } from '@authgate/web-kit/theme';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="auto" theme={theme}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </MantineProvider>
  </StrictMode>,
);
