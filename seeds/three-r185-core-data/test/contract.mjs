import assert from 'node:assert/strict';
import {
  BufferAttribute,
  BufferGeometry,
  Object3D,
  Texture,
} from 'three';

// Normalized integer attributes are decoded when read.
{
  const raw = new BufferAttribute(new Uint8Array([0, 128, 255]), 1);
  const normalized = new BufferAttribute(new Uint8Array([0, 128, 255]), 1, true);

  assert.equal(raw.getX(1), 128);
  assert.equal(normalized.getX(0), 0);
  assert.ok(Math.abs(normalized.getX(1) - 128 / 255) < 1e-12);
  assert.equal(normalized.getX(2), 1);
}

// The attributes object reflects replacement and deletion by name.
{
  const geometry = new BufferGeometry();
  const first = new BufferAttribute(new Float32Array([1, 2, 3]), 3);
  const replacement = new BufferAttribute(new Float32Array([4, 5, 6]), 3);

  assert.strictEqual(geometry.setAttribute('position', first), geometry);
  assert.strictEqual(geometry.attributes.position, first);
  assert.strictEqual(geometry.getAttribute('position'), first);

  geometry.setAttribute('position', replacement);
  assert.strictEqual(geometry.attributes.position, replacement);
  assert.equal(geometry.hasAttribute('position'), true);

  assert.strictEqual(geometry.deleteAttribute('position'), geometry);
  assert.equal(geometry.hasAttribute('position'), false);
  assert.equal(geometry.attributes.position, undefined);
}

// Traversal includes the root and follows depth-first insertion order.
{
  const root = new Object3D();
  root.name = 'root';
  const first = new Object3D();
  first.name = 'first';
  const grandchild = new Object3D();
  grandchild.name = 'grandchild';
  const second = new Object3D();
  second.name = 'second';

  first.add(grandchild);
  root.add(first, second);

  const names = [];
  root.traverse((object) => names.push(object.name));
  assert.deepEqual(names, ['root', 'first', 'grandchild', 'second']);
}

// Texture.image is an alias for Source.data; clone() intentionally shares Source.
{
  const image = { width: 2, height: 3, label: 'original' };
  const texture = new Texture(image);
  const clone = texture.clone();

  assert.strictEqual(texture.image, image);
  assert.strictEqual(texture.source.data, image);
  assert.strictEqual(clone.source, texture.source);
  assert.strictEqual(clone.image, image);

  const replacement = { width: 4, height: 5, label: 'replacement' };
  clone.image = replacement;
  assert.strictEqual(texture.image, replacement);
  assert.strictEqual(texture.source.data, replacement);
}
