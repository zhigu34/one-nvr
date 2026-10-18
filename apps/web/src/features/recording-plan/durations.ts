/**
 * The durations offered for a manual recording (PRD REC-03 allows 1-120).
 *
 * One list for both the per-row control and the batch bar: two copies would
 * eventually offer different choices for the same request, and an operator
 * would have no way to tell which surface was authoritative.
 */
export const MANUAL_DURATIONS = [10, 30, 60, 120] as const
