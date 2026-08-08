(function exposeEncoderOptions(root, factory) {
  const helpers = factory();
  if (typeof module === 'object' && module.exports) module.exports = helpers;
  root.ShrinkrayEncoderOptions = helpers;
}(typeof globalThis === 'undefined' ? this : globalThis, () => {
  function buildEncoderOptions(encoders) {
    const available = encoders && typeof encoders === 'object' ? encoders : {};
    const options = [
      { value: 'auto', label: 'Auto — fastest available' },
      { value: 'software', label: 'Software — best compression' },
    ];
    if (available.qsv === true) options.push({ value: 'qsv', label: 'Intel QSV' });
    if (available.vaapi === true) options.push({ value: 'vaapi', label: 'VAAPI' });
    if (available.nvenc === true) options.push({ value: 'nvenc', label: 'NVIDIA NVENC' });
    return options;
  }

  return { buildEncoderOptions };
}));
