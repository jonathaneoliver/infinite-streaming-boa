/**
 * The four things a transition request can say, in one place.
 *
 * The same escalation is offered on a radio row (everyone on that radio) and on
 * a client card (this one device), and the wording has to match between them:
 * they send the identical frame and differ only in how many clients receive it.
 * Two copies of these sentences would drift the first time one was corrected,
 * and the thing being corrected is usually a promise about what the client
 * does — which is exactly the part a reader trusts.
 *
 * ORDER IS THE ESCALATION. suggest asks, imminent and terminate say something
 * alarming and still only ask, and insist is the one that acts. A row of
 * buttons in this order reads as a ladder, which is what it is.
 */
export interface SteerMode {
  /** The API's `?mode=` value. */
  mode: 'suggest' | 'imminent' | 'terminate' | 'insist';
  /** The button, kept short enough for a crowded row. */
  label: string;
  /** What it promises the client, and what it does not. */
  says: string;
}

export const STEER_MODES: readonly SteerMode[] = [
  {
    mode: 'suggest',
    label: 'steer',
    says:
      'Only a request: nobody is denied or disconnected, and a client that refuses stays here.',
  },
  {
    mode: 'imminent',
    label: 'warn',
    says:
      'Says it is about to be dropped (Disassociation Imminent, no timer). It never is: ' +
      'nothing follows the warning, and a client that refuses stays here.',
  },
  {
    mode: 'terminate',
    label: 'term',
    says:
      'Says this access point is shutting down (BSS Termination Included). It does not: ' +
      'the AP stays up, and a client that refuses stays here.',
  },
  {
    mode: 'insist',
    label: 'force',
    says:
      'Warns it will be dropped in 5s, then disassociates any client still here, which ' +
      'picks its own access point. No deny list, so it may come straight back.',
  },
] as const;
