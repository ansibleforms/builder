// The registry login a build pushes with
import { describe, it, expect } from 'vitest';
import fs from 'fs';
import os from 'os';
import path from 'path';
import { registryHost, writeDockerConfig, insecureRegistry } from '../src/registry.js';

describe('the registry login', () => {
  it('names the registry by its host, and pushes to a plain http one without TLS', () => {
    expect(registryHost('https://registry.example.com/v2/')).toBe('registry.example.com');
    expect(insecureRegistry({ url: 'http://localhost:5000' })).toBe(true);
    expect(insecureRegistry({ url: 'https://registry.example.com' })).toBe(false);
  });

  it('writes a docker config only its owner reads', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'af-reg-'));
    writeDockerConfig(dir, { url: 'https://registry.example.com', user: 'u', password: 'p' });
    const file = path.join(dir, 'config.json');
    expect(JSON.parse(fs.readFileSync(file, 'utf8')).auths['registry.example.com'].auth).toBe(Buffer.from('u:p').toString('base64'));
    expect(fs.statSync(file).mode & 0o077).toBe(0);
  });
});
