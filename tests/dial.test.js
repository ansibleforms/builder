// The builder dials as a builder, and waits as long as the runner does before it dials again
import { describe, it, expect } from 'vitest';
import { builderHeaders, redialWait, noProxy } from '../src/socket/dial.js';
import { CLOSE, REFUSED_REDIAL_MS, CONTRACT_REDIAL_MS } from '../src/socket/protocol.js';

describe('its dial', () => {
  it('says it is a builder, with its token, and its join token when it has one', () => {
    process.env.RTE_JOIN_TOKEN = 'afj_x';
    process.env.RTE_NAME = 'builder-1';
    const h = builderHeaders('token-0123456789abcdef');
    expect(h['X-AF-Peer-Kind']).toBe('builder');
    expect(h.Authorization).toBe('Bearer token-0123456789abcdef');
    expect(h['X-AF-RTE-Name']).toBe('builder-1');
    expect(h['X-AF-RTE-Join']).toBe('afj_x');
  });

  it('waits longer each time, ten minutes once refused, a minute on another contract', () => {
    expect(redialWait({ code: 1006, status: null, delay: 1000, random: 0 })).toEqual({ wait: 1000, delay: 2000, refused: false });
    expect(redialWait({ code: 1006, status: 401, delay: 4000 }).wait).toBe(REFUSED_REDIAL_MS);
    expect(redialWait({ code: CLOSE.CONTRACT, status: null, delay: 1000 }).wait).toBe(CONTRACT_REDIAL_MS);
  });

  it('reaches the hosts NO_PROXY names directly', () => {
    process.env.NO_PROXY = '.internal,forms.example.com';
    expect(noProxy('app.internal')).toBe(true);
    expect(noProxy('forms.example.com')).toBe(true);
    expect(noProxy('other.example.com')).toBe(false);
  });
});
