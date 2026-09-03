/**
 * Turning a failed request into a sentence the operator can act on.
 *
 * The backend speaks English and is not being translated: its messages name
 * concrete objects and rules ("escalation chain has no steps", "unknown
 * timezone: Mars/Olympus"), and paraphrasing those into Russian from the
 * frontend would mean maintaining a second, drifting copy of the engine's
 * validation vocabulary.
 *
 * What is translated is the part the frontend knows on its own: what the status
 * code means. The result leads with that, then quotes the server verbatim —
 * so a Russian-speaking operator always gets a Russian sentence, and never
 * loses the specific reason.
 */

import { ApiError } from '../api/client';
import type { I18nContextValue } from './I18nProvider';

type Translate = I18nContextValue['t'];

function statusMessage(status: number, t: Translate): string {
  switch (status) {
    case 400:
    case 422:
      return t('error.validation');
    case 401:
      return t('error.unauthorized');
    case 403:
      return t('error.forbidden');
    case 404:
      return t('error.notFound');
    case 409:
      return t('error.conflict');
    case 429:
      return t('error.rateLimited');
    case 503:
      return t('error.unavailable');
    default:
      if (status >= 500) return t('error.server');
      return t('error.status', { status });
  }
}

/** One line describing any thrown value, ready to show. */
export function describeError(error: unknown, t: Translate): string {
  if (error instanceof ApiError) {
    const summary = statusMessage(error.status, t);
    // Nothing to append when the server only gave a status: the generated
    // fallback message would just restate the code we already translated.
    if (!error.detail || error.detail === summary) return summary;
    return `${summary}: ${error.detail}`;
  }
  if (error instanceof TypeError) {
    // fetch rejects with a TypeError when the request never reached a server —
    // DNS, TLS, offline. There is no status to describe, and the browser's own
    // message ("Failed to fetch") is both English and unhelpful.
    return t('error.network');
  }
  if (error instanceof Error) return error.message;
  return String(error);
}
