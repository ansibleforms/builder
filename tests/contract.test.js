// The socket contract this builder speaks : the controller's. CONTROLLER_PROTOCOL_URL (CI : the
// controller's latest release) gives the controller's protocol.js ; without it, only its own is read.
import { describe, it, expect } from 'vitest';
import { SOCKET_CONTRACT } from '../src/socket/protocol.js';

describe('its socket contract', () => {
  it('is a number', () => {
    expect(Number.isInteger(SOCKET_CONTRACT)).toBe(true);
  });

  it.runIf(process.env.CONTROLLER_PROTOCOL_URL)('is the controller latest release', async () => {
    const text = await (await fetch(process.env.CONTROLLER_PROTOCOL_URL)).text();
    const theirs = Number((/export const SOCKET_CONTRACT = (\d+);/.exec(text) || [])[1]);
    expect(SOCKET_CONTRACT, 'the controller speaks another contract : release a builder that speaks it').toBe(theirs);
  });
});
