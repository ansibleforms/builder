// The architectures a build asks for, and those BuildKit can build
import { describe, it, expect } from 'vitest';
import { platformsOf, platformsOfWorkers } from '../src/buildkit.js';

describe('the platforms of a build', () => {
  it('reads amd64, arm64 and all, and none as the builder own', () => {
    expect(platformsOf('amd64')).toEqual(['linux/amd64']);
    expect(platformsOf('arm64')).toEqual(['linux/arm64']);
    expect(platformsOf('all')).toEqual(['linux/amd64', 'linux/arm64']);
    expect(platformsOf(undefined)).toEqual([]);
    expect(() => platformsOf('s390x')).toThrow(/no architecture/);
  });

  it('lists what the workers build, each once', () => {
    const workers = [{ platforms: [{ os: 'linux', architecture: 'amd64' }, { os: 'linux', architecture: 'amd64', variant: 'v2' }, { os: 'linux', architecture: '386' }] }];
    expect(platformsOfWorkers(workers)).toEqual(['linux/amd64', 'linux/386']);
    expect(platformsOfWorkers(null)).toEqual([]);
  });
});
