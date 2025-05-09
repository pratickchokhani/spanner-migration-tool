import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { MatDialogRef, MAT_DIALOG_DATA } from '@angular/material/dialog';

import { TuneGcsFormComponent } from './tune-gcs-form.component';

describe('TuneGcsFormComponent', () => {
  let component: TuneGcsFormComponent;
  let fixture: ComponentFixture<TuneGcsFormComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
    declarations: [TuneGcsFormComponent],
    imports: [],
    providers: [
        {
            provide: MatDialogRef,
            useValue: {
                close: () => { },
            },
        },
        {
            provide: MAT_DIALOG_DATA,
            useValue: {}
        },
        provideHttpClient(withInterceptorsFromDi())
    ]
})
    .compileComponents();
  });

  beforeEach(() => {
    fixture = TestBed.createComponent(TuneGcsFormComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  it('should create', () => {
    expect(component).toBeTruthy();
  });
});
