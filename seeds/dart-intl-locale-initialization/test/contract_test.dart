import 'package:csx_intl_locale/dates.dart';
import 'package:intl/intl.dart';
import 'package:test/test.dart';

void main() {
  final when = DateTime.utc(2026, 1, 2);

  test('a non-default locale throws until the date symbols are loaded', () {
    // Matched on the message, not the class: LocaleDataException is thrown
    // from package:intl but is not exported by it, so `on LocaleDataException`
    // does not compile. The message and the runtime type name are the only
    // handles the package gives you.
    expect(
      () => formatIn('ko', when),
      throwsA(predicate((e) =>
          e.runtimeType.toString() == 'LocaleDataException' &&
          e.toString().contains('Locale data has not been initialized'))),
    );
  });

  test('but the same locale formats numbers with no initialization', () {
    // This is why the failure reads as random: half of intl is ready and
    // half of it is not.
    expect(numberIn('de', 1234.5), equals('1.234,5'));
  });

  test('after initializeDateFormatting the locale formats', () async {
    await loadLocales();
    expect(formatIn('ko', when), equals('2026년 1월 2일'));
    expect(DateFormat('EEEE', 'fr').format(when), equals('vendredi'));
  });

  test('parse rolls an impossible date over without saying so', () {
    expect(parseLoose('2026-02-30'), equals(DateTime(2026, 3, 2)));
  });

  test('parseStrict is the one that reports it', () {
    expect(() => parseStrict('2026-02-30'), throwsFormatException);
    expect(parseStrict('2026-02-28'), equals(DateTime(2026, 2, 28)));
  });
}
