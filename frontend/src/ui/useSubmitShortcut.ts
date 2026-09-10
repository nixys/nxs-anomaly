import { useEffect } from 'react';

/**
 * ⌘↵ / Ctrl+↵ submits the open dialog.
 *
 * Every form in this product ends in the same two buttons, and reaching them
 * means leaving the keyboard for the pointer in the middle of typing. The
 * shortcut is the one editors, chat clients and code review tools have taught
 * everybody, so it needs no discovery.
 *
 * It is deliberately not plain Enter: several of these forms contain textareas
 * (a webhook body, a message template), where Enter is a newline and submitting
 * on it would truncate what somebody is writing.
 */
export function useSubmitShortcut(active: boolean, submit: () => void) {
  useEffect(() => {
    if (!active) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
        event.preventDefault();
        submit();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [active, submit]);
}
